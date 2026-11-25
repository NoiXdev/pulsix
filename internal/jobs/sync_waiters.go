package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// SyncWaiters is what waits for a sync_repo job to finish: run requests
// made with sync_first (spec 2026-11-25 A), which become pending once
// their sync succeeded and fail otherwise. The queue tells it about every
// terminal transition of a sync, whatever the outcome, inside the
// transition's transaction (ctx carries it), so what waits moves with the
// job's terminal state or not at all. Implemented by runrequests.Service.
type SyncWaiters interface {
	SyncFinished(ctx context.Context, orgID, jobID uuid.UUID) error
}

// SetSyncWaiters injects what waits for syncs, post-construction for the
// same circularity as SetSyncSink: run requests are built after the queue.
// A nil port tells nobody; what waits then catches up on its own.
func (q *SQLQueue) SetSyncWaiters(w SyncWaiters) { q.syncWaiters = w }

// tellSyncWaiters is terminate's call of the port; a failure rolls the
// transition back, like every other finish-path sink.
func (q *SQLQueue) tellSyncWaiters(ctx context.Context, orgID, jobID uuid.UUID) error {
	if q.syncWaiters == nil {
		return nil
	}
	if err := q.syncWaiters.SyncFinished(ctx, orgID, jobID); err != nil {
		return fmt.Errorf("tell what waits for the sync: %w", err)
	}
	return nil
}
