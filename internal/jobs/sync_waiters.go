package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// SyncWaiters is what waits for a sync_repo job to finish: run requests
// made with sync_first (spec 2026-11-25 A), which become pending once
// their sync succeeded and fail otherwise. The queue tells it about every
// terminal transition of a sync, whatever the outcome, inside the
// transition's transaction (ctx carries it), so what waits moves with the
// job's terminal state; a failure there is logged and left to the run
// requests' sweep (tellSyncWaiters). Implemented by runrequests.Service.
type SyncWaiters interface {
	SyncFinished(ctx context.Context, orgID, jobID uuid.UUID) error
}

// SetSyncWaiters injects what waits for syncs, post-construction for the
// same circularity as SetSyncSink: run requests are built after the queue.
// A nil port tells nobody; what waits then catches up on its own.
func (q *SQLQueue) SetSyncWaiters(w SyncWaiters) { q.syncWaiters = w }

// tellSyncWaiters is terminate's call of the port, under its own savepoint
// (final review M5, the same ruling as reportRunGroup): a failing waiter —
// a store error, or a statement that aborts the transaction — must not
// keep the sync from finishing, or a retried Finish would fail the same
// way forever. Its writes are rolled back to the savepoint, the failure is
// logged, and the run requests' sweep moves the waiting requests later.
func (q *SQLQueue) tellSyncWaiters(ctx context.Context, orgID, jobID uuid.UUID) {
	if q.syncWaiters == nil {
		return
	}
	if _, err := q.db.Exec(ctx, `SAVEPOINT sync_waiters`); err != nil {
		slog.ErrorContext(ctx, "sync finished: open waiters savepoint", "job", jobID, "org", orgID, "err", err)
		return
	}
	if err := q.callSyncWaiters(ctx, orgID, jobID); err != nil {
		if _, rbErr := q.db.Exec(ctx, `ROLLBACK TO SAVEPOINT sync_waiters`); rbErr != nil {
			slog.ErrorContext(ctx, "sync finished: roll back waiters savepoint", "job", jobID, "org", orgID, "err", rbErr)
			return
		}
		slog.ErrorContext(ctx, "sync finished: telling the waiting run requests failed; the sweep moves them", "job", jobID, "org", orgID, "err", err)
		return
	}
	if _, err := q.db.Exec(ctx, `RELEASE SAVEPOINT sync_waiters`); err != nil {
		slog.ErrorContext(ctx, "sync finished: release waiters savepoint", "job", jobID, "org", orgID, "err", err)
	}
}

// callSyncWaiters calls the port, turning a panic into an error.
func (q *SQLQueue) callSyncWaiters(ctx context.Context, orgID, jobID uuid.UUID) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the sync waiters panicked: %v", p)
		}
	}()
	return q.syncWaiters.SyncFinished(ctx, orgID, jobID)
}
