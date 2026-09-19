package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// RequeueWorkerJobs hands a revoked worker's jobs back (spec 2026-09-18 §4
// "Revocation"): claimed and running jobs below maxAttempts return to the
// queue with the reason recorded in error — the claim already counted the
// attempt — exhausted ones end as error with the reason, and cancelling
// ones as cancelled. The ended ones go through the shared terminal
// transition, so their payload is wiped and the finish-path sinks run.
// The requeued jobs' in-process stream and cancel records are dropped, so
// the reaper does not count the cut streams as alive.
func (q *SQLQueue) RequeueWorkerJobs(ctx context.Context, orgID, workerID uuid.UUID, reason string, maxAttempts int) (requeued, ended int, err error) {
	ctx = db.WithOrgScope(ctx, orgID)
	g := gen.New(q.db)
	worker := pgtype.UUID{Bytes: workerID, Valid: true}
	ids, err := g.RequeueWorkerJobs(ctx, gen.RequeueWorkerJobsParams{
		Reason: "requeued: " + reason, OrgID: orgID, WorkerID: worker, MaxAttempts: int32(maxAttempts), //nolint:gosec // a small configured count
	})
	if err != nil {
		return 0, 0, fmt.Errorf("requeue worker jobs: %w", err)
	}
	for _, id := range ids {
		q.streams.remove(id)
		q.cancels.remove(id)
	}
	rest, err := g.ListWorkerJobsToEnd(ctx, gen.ListWorkerJobsToEndParams{OrgID: orgID, WorkerID: worker, MaxAttempts: int32(maxAttempts)}) //nolint:gosec // as above
	if err != nil {
		return len(ids), 0, fmt.Errorf("list worker jobs to end: %w", err)
	}
	for _, r := range rest {
		// Bound to the revoked worker: a job that moved on meanwhile (a
		// reaper requeue and another worker's claim) is left alone.
		t := terminalTransition{jobID: r.ID, status: StatusError, errText: reason, from: []Status{StatusClaimed, StatusRunning}, worker: &workerID}
		if Status(r.Status) == StatusCancelling {
			t.status, t.from = StatusCancelled, []Status{StatusCancelling}
		}
		applied, err := q.terminate(ctx, t)
		if err != nil {
			return len(ids), ended, fmt.Errorf("end job %s: %w", r.ID, err)
		}
		if applied {
			ended++
		}
	}
	return len(ids), ended, nil
}
