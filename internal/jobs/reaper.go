// The reaper: the manager-side safety net for claims whose worker went
// away. One sweep: requeue claimed/running jobs whose claim is stale and
// whose stream is not alive, and fail cancelling jobs whose worker never
// confirmed the cancel within the deadline.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
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
	// DefaultMaxAttempts is how often a job may be claimed before a stale
	// claim fails it instead of requeuing it (PULSIX_JOB_MAX_ATTEMPTS).
	DefaultMaxAttempts = 3
	// unreportedGroupWindow bounds how far back the lost-report sweep
	// looks. Long enough to cover a manager restart and then some;
	// short enough that a notification about a run nobody is still
	// waiting on is never sent (backlog 2.87).
	unreportedGroupWindow = 6 * time.Hour
	// unreportedGroupBatch caps one pass, so a backlog is worked off
	// over several ticks instead of in one long transaction storm.
	unreportedGroupBatch = 20
)

// Reaper sweeps the queue on an interval. It is part of the queue, not the
// service: it works org-unscoped, on the same pool, and reads the
// in-process stream registry — the source of truth for "a worker is still
// attached" (multi-instance managers are v0.6; a restart unbinds every
// stream, and the next sweep requeues whatever was mid-run).
type Reaper struct {
	q           *SQLQueue
	interval    time.Duration
	maxAttempts int
}

// NewReaper builds the reaper; interval <= 0 keeps DefaultReaperInterval,
// maxAttempts <= 0 keeps DefaultMaxAttempts.
func NewReaper(q *SQLQueue, interval time.Duration, maxAttempts int) *Reaper {
	if interval <= 0 {
		interval = DefaultReaperInterval
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Reaper{q: q, interval: interval, maxAttempts: maxAttempts}
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

// Sweep does one pass and returns how many jobs were requeued and how many
// failed: the stale requeue in one transaction, then every failure through
// the queue's terminal transition (payload wipe and sinks included). A requeued or failed job loses its stream
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
		StaleBefore: now.Add(-claimStaleAfter), Live: live, MaxAttempts: int32(r.maxAttempts),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("requeue stale claims: %w", err)
	}
	requeued = len(stale)
	exhausted, err := g.ListExhaustedStaleJobs(ctx, gen.ListExhaustedStaleJobsParams{
		StaleBefore: now.Add(-claimStaleAfter), Live: live, MaxAttempts: int32(r.maxAttempts),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list exhausted stale claims: %w", err)
	}

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
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit reaper sweep: %w", err)
	}
	for _, id := range stale {
		r.q.streams.removeAll(id)
		r.q.cancels.remove(id)
	}
	// Exhausted stale claims fail through the one terminal transition, each
	// in its own org-bound transaction: the sealed payload is wiped and the
	// sinks run exactly as on a worker's finish. The transition re-checks
	// staleness inside its UPDATE with the same cutoff this sweep's scan
	// used (backlog 2.53): a job re-claimed between the scan and the write
	// is skipped, not failed. One job's failure does not stop the others;
	// the first error is reported after the pass.
	var firstErr error
	for _, e := range exhausted {
		applied, err := r.q.terminate(ctx, terminalTransition{
			jobID: e.ID, status: StatusError,
			errText:     fmt.Sprintf("the job was claimed %d times and its worker never reported a result; giving up (the claim may be too large for the worker, or the run crashes it)", e.Attempts),
			from:        []Status{StatusClaimed, StatusRunning},
			staleBefore: now.Add(-claimStaleAfter), minAttempts: r.maxAttempts,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("fail exhausted job %s: %w", e.ID, err)
			}
			continue
		}
		if applied {
			failed++
		}
	}
	// Unconfirmed cancels fail through the one terminal transition too.
	for _, id := range expired {
		applied, err := r.q.terminate(ctx, terminalTransition{
			jobID: id, status: StatusFailed, errText: "cancel was requested but the worker never confirmed it",
			from: []Status{StatusCancelling},
		})
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("fail unconfirmed cancel %s: %w", id, err)
			}
			continue
		}
		if applied {
			failed++
		}
	}
	// Finally, the run groups whose report was lost to a crash (backlog
	// 2.87). Here rather than in a ticker of its own: this loop already
	// runs on a short interval, already has no organization of its own,
	// and already repairs what a dead manager left behind — which is
	// exactly what an unreported finished group is. A failure of this
	// pass never fails the sweep: the requeues and terminal transitions
	// above matter more than a late notification.
	if _, err := r.q.SweepUnreportedRunGroups(ctx, now.Add(-unreportedGroupWindow), unreportedGroupBatch); err != nil {
		slog.ErrorContext(ctx, "sweep unreported run groups", "err", err)
	}
	if firstErr != nil {
		return requeued, failed, firstErr
	}
	return requeued, failed, nil
}
