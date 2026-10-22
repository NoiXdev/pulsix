// Run groups: one run of a job template is split into one job per zone,
// all sharing run_group_id. Notifications (spec 2026-09-18 §7) are about
// the run, not a zone's job, so the queue reports a run group once — when
// its last job becomes terminal — through the optional RunGroupSink.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
)

// RunGroupResult summarises a run group whose jobs are all terminal.
// Host counts come from the per-host results the worker stored in each
// job's stats (changed_hosts, failed_hosts, unreachable_hosts), counted
// only for hosts the job itself targeted — see HostsChanged.
type RunGroupResult struct {
	OrgID, GroupID uuid.UUID
	// FirstJobID is the group's oldest job: the run view link.
	FirstJobID uuid.UUID
	TemplateID *uuid.UUID
	Trigger    string
	TriggerRef *uuid.UUID
	// Status aggregates the jobs: failed when any failed or errored,
	// otherwise cancelled when any was cancelled, otherwise succeeded.
	Status Status
	// Jobs is the group's job count and Hosts the targeted hosts across
	// all of them (the sum of their host_ids).
	Jobs, Hosts int
	// HostsChanged and HostsFailed count DISTINCT hosts of the group that
	// the workers' stats lists name AND that the reporting job actually
	// targeted. The intersection matters: those lists carry host UUIDs
	// only where the claim's connection map named one, and a host it does
	// not name — an implicit localhost task — stays a bare name
	// (internal/worker/runner/stats.go). Counting the lists raw mixed
	// those names into a total meant to be comparable with Hosts, which
	// counts host_ids. RetryFailed's failedHostIDs intersects the same
	// lists for the same reason (backlog 2.81).
	HostsChanged, HostsFailed int
	// HostsNoResult counts the targeted hosts of jobs that produced no
	// per-host stats at all — a job that ended error or cancelled never
	// got as far as a recap. They are neither OK nor failed: this run
	// reached no verdict on them. Without this, HostsOK absorbed them and
	// a group whose every job died at the worker reported "N hosts (ok N,
	// failed 0)" under a failed result (backlog 2.81).
	HostsNoResult         int
	StartedAt, FinishedAt *time.Time
}

// HostsOK is the number of targeted hosts this run reached a verdict on
// and that verdict was not a failure. Hosts a job never reported on
// (HostsNoResult) are excluded rather than assumed fine.
func (r RunGroupResult) HostsOK() int { return max(r.Hosts-r.HostsFailed-r.HostsNoResult, 0) }

// RunGroupSink receives each finished run group exactly once per terminal
// transition that completes it; ctx carries the finishing transaction, so
// what the sink writes commits with the job's terminal status. A sink
// error never fails the finish: it is logged and the notification lost —
// a notification must not wedge a job in running.
type RunGroupSink interface {
	RunGroupFinished(ctx context.Context, r RunGroupResult) error
}

// SetRunGroupSink injects the finished-run-group port (the notification
// outbox), the same post-construction wiring the other sinks use. Nil
// reports nothing and takes no group lock for run jobs.
func (q *SQLQueue) SetRunGroupSink(sink RunGroupSink) { q.runGroups = sink }

// runGroupQuery reads a run group inside the finishing transaction.
// jsonb_typeof guards the stats lists: a worker that found no host in a
// state writes JSON null there (a nil Go slice), not an empty array.
//
// The changed and failed counts intersect each job's lists with that
// job's OWN host_ids (backlog 2.81): the lists carry bare host names
// wherever the claim's connection map did not supply a uuid, and those
// names must not be counted into a total compared against host_ids.
// pg_input_is_valid screens an element before the cast, since a bare name
// is not a uuid and the cast alone would error the whole statement — and
// an errored statement here aborts the finishing transaction.
//
// no_result sums the targeted hosts of jobs whose five per-host stats
// lists are ALL empty — the shape of a job that ended error or cancelled
// without a recap. Deliberately keyed on the absence of stats rather than
// on status: a job that marked itself failed but died before writing a
// recap has reached no per-host verdict either, and status alone would
// read it as one.
const runGroupQuery = `
WITH g AS (
  SELECT id, status, host_ids, started_at, finished_at, created_at, template_id, trigger, trigger_ref, stats
  FROM jobs WHERE org_id = $1 AND run_group_id = $2 AND kind = 'run'
),
lists AS (
  SELECT
    host_ids,
    CASE WHEN jsonb_typeof(stats->'changed_hosts') = 'array' THEN stats->'changed_hosts' ELSE '[]'::jsonb END AS changed,
    (CASE WHEN jsonb_typeof(stats->'failed_hosts') = 'array' THEN stats->'failed_hosts' ELSE '[]'::jsonb END) ||
    (CASE WHEN jsonb_typeof(stats->'unreachable_hosts') = 'array' THEN stats->'unreachable_hosts' ELSE '[]'::jsonb END) AS failed,
    (CASE WHEN jsonb_typeof(stats->'ok_hosts') = 'array' THEN stats->'ok_hosts' ELSE '[]'::jsonb END) ||
    (CASE WHEN jsonb_typeof(stats->'skipped_hosts') = 'array' THEN stats->'skipped_hosts' ELSE '[]'::jsonb END) AS unremarkable
  FROM g
)
SELECT
  (SELECT count(*) FROM g),
  (SELECT count(*) FROM g WHERE status IN ('succeeded', 'failed', 'cancelled', 'error')),
  (SELECT count(*) FROM g WHERE status IN ('failed', 'error')),
  (SELECT count(*) FROM g WHERE status = 'cancelled'),
  (SELECT COALESCE(sum(cardinality(host_ids)), 0) FROM g),
  (SELECT count(DISTINCT h) FROM lists, jsonb_array_elements_text(lists.changed) AS h
     WHERE pg_input_is_valid(h, 'uuid') AND h::uuid = ANY(lists.host_ids)),
  (SELECT count(DISTINCT h) FROM lists, jsonb_array_elements_text(lists.failed) AS h
     WHERE pg_input_is_valid(h, 'uuid') AND h::uuid = ANY(lists.host_ids)),
  (SELECT COALESCE(sum(cardinality(host_ids)), 0) FROM lists
     WHERE jsonb_array_length(changed) = 0 AND jsonb_array_length(failed) = 0 AND jsonb_array_length(unremarkable) = 0),
  (SELECT min(started_at) FROM g),
  (SELECT max(finished_at) FROM g),
  (SELECT id FROM g ORDER BY created_at, id LIMIT 1),
  (SELECT template_id FROM g LIMIT 1),
  (SELECT trigger FROM g LIMIT 1),
  (SELECT trigger_ref FROM g LIMIT 1)`

// recordRunGroup hands a completed run group to the sink, inside the
// finishing transaction, which already holds the group's advisory lock
// (terminate): with the lock, the job that completes the group always
// sees every other member terminal, and two members finishing together
// cannot both miss the completion.
//
// The lock alone is not enough (fix round 1, Important #1): CancelJob
// commits a queued job's queued -> cancelled transition as a standalone
// statement, with no group lock of its own — a concurrent member finish
// can see that committed cancel and report the group before the cancel's
// own (locked) completeness check gets a turn, and that check then finds
// the group complete too and reports again. A second report must be
// impossible whatever the path, so before calling the sink this claims
// the group with claimRunGroupReport — an ordinary UPDATE that serializes
// on Postgres's own row lock regardless of which caller holds the
// advisory lock, or whether it is held at all.
func (q *SQLQueue) recordRunGroup(ctx context.Context, orgID, groupID uuid.UUID) error {
	if q.runGroups == nil {
		return nil
	}
	r, won, err := q.claimFinishedRunGroup(ctx, orgID, groupID)
	if err != nil {
		// Neither the read nor the claim may wedge the job in running —
		// the same controller ruling reportRunGroup's savepoint already
		// implements for the sink, and now actually true here too
		// (final review, Important #1): both statements ran inside a
		// savepoint, so the finishing transaction survives their failure
		// and the terminal transition still commits. This transition's
		// report is lost, never retried, since the job it belonged to is
		// terminal by the time any retry could run.
		slog.ErrorContext(ctx, "record run group: read or claim failed; its notifications are lost", "group", groupID, "org", orgID, "err", err)
		return nil
	}
	if !won {
		return nil // the group is not done yet, or another path reported it
	}
	q.reportRunGroup(ctx, orgID, groupID, r)
	return nil
}

// claimFinishedRunGroup runs the group read and the report claim inside
// one savepoint (final review, Important #1). Both statements used to run
// directly on the finishing transaction: a Postgres error in either — the
// review's scenario is an operator statement_timeout that runGroupQuery's
// four count(DISTINCT ...) sub-selects exceed, but an RLS denial or a bad
// cast does the same — left the transaction in the aborted state, so
// clearPayload (the next statement in terminate) failed with "current
// transaction is aborted", the whole finish rolled back, and the job kept
// its non-terminal status. A retried Finish then failed identically, so
// the job stayed running forever and the schedule's overlap guard skipped
// every further occurrence of that schedule.
//
// The savepoint is the same machinery reportRunGroup uses for the sink,
// for the same reason: ROLLBACK TO SAVEPOINT is the one statement Postgres
// still accepts in an aborted transaction.
//
// It reports won = false without an error for a group that is not complete
// yet and for one another path has already claimed; neither is a failure.
func (q *SQLQueue) claimFinishedRunGroup(ctx context.Context, orgID, groupID uuid.UUID) (RunGroupResult, bool, error) {
	r := RunGroupResult{OrgID: orgID, GroupID: groupID}
	if _, err := q.db.Exec(ctx, `SAVEPOINT run_group_read`); err != nil {
		return r, false, fmt.Errorf("open read savepoint: %w", err)
	}
	r, won, err := q.readAndClaimRunGroup(ctx, r, orgID, groupID)
	if err != nil {
		if _, rbErr := q.db.Exec(ctx, `ROLLBACK TO SAVEPOINT run_group_read`); rbErr != nil {
			return r, false, fmt.Errorf("%w (rolling back to the savepoint also failed: %w)", err, rbErr)
		}
		return r, false, err
	}
	if _, err := q.db.Exec(ctx, `RELEASE SAVEPOINT run_group_read`); err != nil {
		// The statements themselves succeeded, so the claim (if it won)
		// stands; a failed release means the connection is gone, and
		// reporting through it would fail too.
		slog.ErrorContext(ctx, "record run group: release read savepoint", "group", groupID, "org", orgID, "err", err)
		return r, false, nil
	}
	return r, won, nil
}

// readAndClaimRunGroup reads the group's aggregate and, when the group is
// complete, claims its single report. Its caller wraps it in a savepoint.
//
// orgID and groupID are the caller's own — terminate and
// recordCancelledRunGroup both already look up org_id, kind and
// run_group_id for their own purposes (the lock condition, the kind
// gate), so this does not re-read them (fix round 1, Minor #3).
func (q *SQLQueue) readAndClaimRunGroup(ctx context.Context, r RunGroupResult, orgID, groupID uuid.UUID) (RunGroupResult, bool, error) {
	var total, terminal, failed, cancelled int
	var first *uuid.UUID
	if err := q.db.QueryRow(ctx, runGroupQuery, orgID, groupID).Scan(
		&total, &terminal, &failed, &cancelled, &r.Hosts, &r.HostsChanged, &r.HostsFailed, &r.HostsNoResult,
		&r.StartedAt, &r.FinishedAt, &first, &r.TemplateID, &r.Trigger, &r.TriggerRef,
	); err != nil {
		return r, false, fmt.Errorf("read group: %w", err)
	}
	if total == 0 || terminal < total || first == nil {
		return r, false, nil // the group is not done yet
	}
	r.Jobs, r.FirstJobID = total, *first
	won, err := q.claimRunGroupReport(ctx, r.FirstJobID)
	if err != nil {
		return r, false, fmt.Errorf("claim report: %w", err)
	}
	if !won {
		return r, false, nil // another path already reported this group
	}
	switch {
	case failed > 0:
		r.Status = StatusFailed
	case cancelled > 0:
		r.Status = StatusCancelled
	default:
		r.Status = StatusSucceeded
	}
	return r, true, nil
}

// claimRunGroupReport is the group's exactly-once guard: an ordinary
// UPDATE that flips run_group_reported from false to true on the group's
// first job (its stable anchor row — the set of jobs in a group, and so
// which one is oldest, never changes after enqueue), reporting whether
// THIS call is the one that flipped it. Postgres serializes two
// concurrent UPDATEs of the same row on its own row lock, so at most one
// caller ever wins, independently of the advisory lock recordRunGroup's
// callers may or may not hold (migration 00031_run_group_reported.sql).
func (q *SQLQueue) claimRunGroupReport(ctx context.Context, firstJobID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE jobs SET run_group_reported = true WHERE id = $1 AND run_group_reported = false`, firstJobID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// reportRunGroup calls the sink under its own savepoint (fix round 1,
// Important #2). The sink receives the finishing transaction through ctx
// and may write through it — a real database error there (a constraint
// violation, an RLS denial, a bad cast) would otherwise abort the whole
// transaction: the next statement (clearPayload) fails with "current
// transaction is aborted", terminate rolls everything back, and a retried
// Finish fails the identical way forever, wedging the job in running.
// Controller ruling (fix round 1): losing one notification is better than
// a stuck job. The savepoint's rollback undoes only the sink's own
// writes — the group-report claim above and the terminal transition that
// follows still commit.
func (q *SQLQueue) reportRunGroup(ctx context.Context, orgID, groupID uuid.UUID, r RunGroupResult) {
	if _, err := q.db.Exec(ctx, `SAVEPOINT run_group_sink`); err != nil {
		slog.ErrorContext(ctx, "finished run group: open sink savepoint", "group", groupID, "org", orgID, "err", err)
		return
	}
	if err := q.callSink(ctx, r); err != nil {
		if _, rbErr := q.db.Exec(ctx, `ROLLBACK TO SAVEPOINT run_group_sink`); rbErr != nil {
			slog.ErrorContext(ctx, "finished run group: roll back sink savepoint", "group", groupID, "org", orgID, "err", rbErr)
			return
		}
		slog.ErrorContext(ctx, "finished run group: the notification sink failed; its notifications are lost", "group", groupID, "org", orgID, "err", err)
		return
	}
	if _, err := q.db.Exec(ctx, `RELEASE SAVEPOINT run_group_sink`); err != nil {
		slog.ErrorContext(ctx, "finished run group: release sink savepoint", "group", groupID, "org", orgID, "err", err)
	}
}

// recordCancelledRunGroup is recordRunGroup for a job cancelled before it
// was claimed: that cancel is a single UPDATE (CancelJob), not a terminal
// transition, so the group check runs in its own transaction under the
// same group lock terminate takes.
func (q *SQLQueue) recordCancelledRunGroup(ctx context.Context, jobID uuid.UUID) {
	if q.runGroups == nil {
		return
	}
	var orgID, groupID uuid.UUID
	var kind string
	if err := q.db.QueryRow(ctx, `SELECT org_id, kind, run_group_id FROM jobs WHERE id = $1`, jobID).Scan(&orgID, &kind, &groupID); err != nil {
		slog.ErrorContext(ctx, "cancelled run group: look up job", "job", jobID, "err", err)
		return
	}
	if Kind(kind) != KindRun {
		return
	}
	if err := q.db.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		if _, err := q.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, finishGroupLockKeyPrefix+groupID.String()); err != nil {
			return fmt.Errorf("lock run group: %w", err)
		}
		return q.recordRunGroup(ctx, orgID, groupID)
	}); err != nil {
		slog.ErrorContext(ctx, "cancelled run group: record", "job", jobID, "err", err)
	}
}

// callSink calls the sink and turns a panic in it into an ordinary sink
// error, so the savepoint machinery above rolls its writes back and the
// finish carries on (final review, Important #6).
//
// There is no recovery net above this: the call path is the worker's
// Finish RPC, grpcsrv installs only the auth interceptors, and grpc-go
// does not recover handler panics itself — so an unrecovered panic in the
// notification outbox killed the whole manager process. That is strictly
// worse than the wedge RunGroupSink's own doc comment exists to prevent,
// and this is the same recover every sibling background path already has.
func (q *SQLQueue) callSink(ctx context.Context, r RunGroupResult) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(ctx, "finished run group: the notification sink panicked", "group", r.GroupID, "org", r.OrgID, "panic", rec, "stack", string(debug.Stack()))
			err = errors.New("the notification sink failed unexpectedly (see the manager log)")
		}
	}()
	return q.runGroups.RunGroupFinished(ctx, r)
}
