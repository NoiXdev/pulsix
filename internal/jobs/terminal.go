// The one terminal transition (final review I2): every path that moves a
// job into a terminal state — the worker's finish, a payload build that
// fails at claim time, the reaper's failures — runs through terminate, so
// the at-rest payload wipe and the finish-path sinks can never diverge
// between paths.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// terminalTransition is one move of a job into a terminal state.
type terminalTransition struct {
	jobID   uuid.UUID
	status  Status
	stats   json.RawMessage // nil keeps the row's stats
	errText string
	// varSources is the variable sources report a sync_repo result carried
	// (finish takes it out of the stats); nil: none was sent. Only a
	// succeeded sync records it (recordSync).
	varSources json.RawMessage
	// from is the set of current statuses the transition applies to; a job
	// in any other status is left untouched and onMiss decides the answer.
	from []Status
	// worker binds the transition to the claiming worker (nil: the
	// manager's own transition, any worker_id matches).
	worker *uuid.UUID
	// staleBefore and minAttempts arm the exhaustion transition's
	// staleness re-check (backlog 2.53): when staleBefore is set, the
	// status write itself additionally requires claimed_at <
	// staleBefore AND attempts >= minAttempts — the reaper passes the
	// same cutoff its scan used, so a job re-claimed between the reaper's
	// scan and this write is skipped instead of failed. Zero disables the
	// re-check (every other caller).
	staleBefore time.Time
	minAttempts int
	// onMiss runs inside the transaction when no row matched; nil means a
	// miss is not an error (the job moved on meanwhile).
	onMiss func(ctx context.Context) error
}

// terminate applies t in ONE transaction bound to the job's organization:
// the status write, every finish-path sink (the repository's sync state,
// the connection-test row, the rotation group's finalize) and the wipe of
// the sealed payload. A sink failure rolls everything back and returns the
// error — the job keeps its status and payload, and a retry completes all
// of it. applied reports whether the status was written. After a commit
// the job's in-process stream and cancel records are dropped, and a
// succeeded sync whose sink stored new variable sources has them applied
// (applySources, finish_sources.go) — outside the transaction, so the applier sees the
// committed record and its failure cannot undo the sync.
//
// Members of a rotation group — and of a run group when a RunGroupSink is
// wired (run_group.go) — serialize on a per-group advisory transaction
// lock taken before the status write: with the status and the group's
// completeness count in one transaction, two members finishing
// concurrently would otherwise each count the other as still running and
// neither would finalize (or report) the group.
func (q *SQLQueue) terminate(ctx context.Context, t terminalTransition) (bool, error) {
	if !t.status.Terminal() {
		return false, fmt.Errorf("%w: %q is not a terminal status", ErrInvalidInput, t.status)
	}
	// org_id, kind and run_group_id never change after insert, so reading
	// them before the transaction is safe; the org binds the transaction.
	var orgID, groupID uuid.UUID
	var kind string
	if err := q.db.Pool().QueryRow(ctx,
		`SELECT org_id, kind, run_group_id FROM jobs WHERE id = $1`, t.jobID,
	).Scan(&orgID, &kind, &groupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("look up job: %w", err)
	}
	from := make([]string, len(t.from))
	for i, s := range t.from {
		from[i] = string(s)
	}
	var stats []byte
	if t.stats != nil {
		stats = t.stats
	}
	applied := false
	var applyRepo uuid.UUID
	err := q.db.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		if Kind(kind) == KindRotateKey || Kind(kind) == KindConnectionTest || (Kind(kind) == KindRun && q.runGroups != nil) {
			if _, err := q.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
				finishGroupLockKeyPrefix+groupID.String()); err != nil {
				return fmt.Errorf("lock run group: %w", err)
			}
		}
		matched := false
		if t.staleBefore.IsZero() {
			tag, err := q.db.Exec(ctx,
				`UPDATE jobs SET status = $2, stats = COALESCE($3::jsonb, stats), error = $4, finished_at = $5
				 WHERE id = $1 AND status = ANY($6::text[])
				   AND ($7::uuid IS NULL OR worker_id = $7::uuid)`,
				t.jobID, string(t.status), stats, t.errText, q.now(), from, t.worker)
			if err != nil {
				return fmt.Errorf("finish job: %w", err)
			}
			matched = tag.RowsAffected() > 0
		} else {
			// The exhaustion write re-checks staleness inside the UPDATE
			// (backlog 2.53): claimed_at must still be behind the cutoff
			// the reaper's scan used, so a re-claim between the scan and
			// this write — or any other status move — leaves the row
			// alone.
			var worker pgtype.UUID
			if t.worker != nil {
				worker = pgtype.UUID{Bytes: *t.worker, Valid: true}
			}
			rows, err := gen.New(q.db).FailExhaustedStaleJob(ctx, gen.FailExhaustedStaleJobParams{
				FailedStatus: string(t.status),
				Stats:        stats,
				ErrText:      t.errText,
				FinishedAt:   pgtype.Timestamptz{Time: q.now(), Valid: true},
				ID:           t.jobID,
				FromStatuses: from,
				WorkerID:     worker,
				StaleBefore:  t.staleBefore,
				MaxAttempts:  int32(t.minAttempts), //nolint:gosec // a small configured count
			})
			if err != nil {
				return fmt.Errorf("finish job: %w", err)
			}
			matched = rows > 0
		}
		if !matched {
			if t.onMiss != nil {
				return t.onMiss(ctx)
			}
			return nil
		}
		// A job that did not succeed releases nothing that waits on it:
		// its queued dependents are cancelled first, so the rotation
		// group's completeness count below already sees them terminal.
		if err := q.cancelDependents(ctx, t.jobID, t.status); err != nil {
			return err
		}
		repoID, err := q.recordSync(ctx, t.jobID, t.status, t.errText, t.varSources)
		if err != nil {
			return err
		}
		applyRepo = repoID
		if err := q.recordTest(ctx, t.jobID, t.status); err != nil {
			return err
		}
		if err := q.recordRotation(ctx, t.jobID, t.status); err != nil {
			return err
		}
		if Kind(kind) == KindRun {
			if err := q.recordRunGroup(ctx, orgID, groupID); err != nil {
				return err
			}
		}
		// The terminal payload's at-rest lifetime ends here: the sealed
		// one-time login / pending rotation key drops once the job is
		// terminal (see clearPayload).
		if err := q.clearPayload(ctx, t.jobID); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if applied {
		q.streams.removeAll(t.jobID)
		q.cancels.remove(t.jobID)
		if applyRepo != uuid.Nil {
			q.applySources(ctx, orgID, applyRepo)
		}
	}
	return applied, nil
}
