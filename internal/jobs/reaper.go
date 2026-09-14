// The reaper: the manager-side safety net for claims whose worker went
// away. One sweep, one transaction: requeue claimed/running jobs whose
// claim is stale and whose stream is not alive, and fail cancelling jobs
// whose worker never confirmed the cancel within the deadline.
package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// Reaper deadlines. A claim older than claimStaleAfter whose stream shows
// no sign of life is requeued (at-least-once: ansible is idempotent-
// friendly, and the run view shows the restart). A cancel whose worker
// never confirmed within cancelConfirmAfter fails the job.
const (
	claimStaleAfter       = 60 * time.Second
	cancelConfirmAfter    = 60 * time.Second
	DefaultReaperInterval = 10 * time.Second
)

// Reaper sweeps the queue on an interval. It is part of the queue, not the
// service: it works org-unscoped, on the same pool, and reads the
// in-process stream registry — the source of truth for "a worker is still
// attached" (multi-instance managers are v0.6; a restart unbinds every
// stream, and the next sweep requeues whatever was mid-run).
type Reaper struct {
	q        *SQLQueue
	interval time.Duration
}

// NewReaper builds the reaper; interval <= 0 keeps DefaultReaperInterval.
func NewReaper(q *SQLQueue, interval time.Duration) *Reaper {
	if interval <= 0 {
		interval = DefaultReaperInterval
	}
	return &Reaper{q: q, interval: interval}
}

// Run sweeps every interval until ctx is done.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _, _ = r.Sweep(ctx)
		}
	}
}

// Sweep does one pass in one transaction and returns how many jobs were
// requeued and how many failed. A requeued or failed job loses its stream
// binding and cancel-deadline record: the old claim is void either way.
func (r *Reaper) Sweep(ctx context.Context) (requeued, failed int, err error) {
	now := r.q.now()
	live := r.q.streams.aliveIDs(now.Add(-claimStaleAfter))
	if live == nil {
		live = []uuid.UUID{}
	}
	tx, err := r.q.db.Pool().Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin reaper transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	g := gen.New(tx)

	stale, err := g.RequeueStaleJobs(ctx, gen.RequeueStaleJobsParams{
		StaleBefore: now.Add(-claimStaleAfter), Live: live,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("requeue stale claims: %w", err)
	}
	requeued = len(stale)

	cancelling, err := g.ListCancellingJobs(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list cancelling jobs: %w", err)
	}
	var expired []uuid.UUID
	for _, c := range cancelling {
		ref, ok := r.q.cancels.get(c.ID)
		if !ok {
			// No recorded cancel request (a restart wiped it): fall back
			// to the claim time, which can only make the deadline earlier.
			if t := db.TimePtr(c.ClaimedAt); t != nil {
				ref = *t
			}
		}
		if ref.Before(now.Add(-cancelConfirmAfter)) {
			expired = append(expired, c.ID)
		}
	}
	if len(expired) > 0 {
		tag, err := tx.Exec(ctx,
			`UPDATE jobs SET status = 'failed', error = $2, finished_at = $3
			 WHERE id = ANY($1::uuid[]) AND status = 'cancelling'`,
			expired, "cancel was requested but the worker never confirmed it", now)
		if err != nil {
			return 0, 0, fmt.Errorf("fail unconfirmed cancels: %w", err)
		}
		failed = int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit reaper sweep: %w", err)
	}
	for _, id := range stale {
		r.q.streams.remove(id)
		r.q.cancels.remove(id)
	}
	for _, id := range expired {
		r.q.streams.remove(id)
		r.q.cancels.remove(id)
	}
	return requeued, failed, nil
}
