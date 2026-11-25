package runrequests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// MaxSyncWait bounds how long a request waits for its sync (spec
// 2026-11-25 A, "sync_first"): a sync still queued or running after it,
// one whose worker keeps being lost and requeued included, fails the
// request. The request can be asked for again.
const MaxSyncWait = time.Hour

// SweepInterval is how often the manager's sweep (RunSweeper) looks at the
// waiting requests. The sync's own finish moves its requests at once
// (SyncFinished); the sweep catches the rest.
const SweepInterval = 10 * time.Second

// Syncer starts the sync a sync_first request waits for.
type Syncer interface {
	// SyncFirst queues a sync of the repository the template's playbook
	// comes from as actor, or finds the one queued, and says which; nil
	// when the playbook comes from no repository.
	SyncFirst(ctx context.Context, orgID, templateID, actor uuid.UUID) (*SyncStart, error)
}

// SyncStart is the sync a sync_first request waits for: its job, the
// repository, and whether it was already queued (the request attached to
// it) rather than queued for this request.
type SyncStart struct {
	JobID         uuid.UUID
	RepoID        uuid.UUID
	AlreadyQueued bool
}

// The reasons a waiting request fails with (Request.SyncError).
const (
	syncFailedReason    = "the repository's sync failed"
	syncCancelledReason = "the repository's sync was cancelled"
	syncGoneReason      = "the repository's sync job no longer exists"
	syncTimeoutReason   = "the repository's sync did not finish within 1 hour"
)

// syncFirst queues the sync r waits for (or attaches it to the one queued
// and not yet taken by a worker) and makes r wait for it, unless the template has nothing to
// sync. A token over its cap is refused before a sync starts; the store's
// atomic count still decides when two creates race.
func (s *Service) syncFirst(ctx context.Context, r *Request) (*SyncStart, error) {
	if r.RequestedByToken != nil {
		n, err := s.store.CountPending(ctx, r.OrgID, r.RequestedByToken, s.now())
		if err != nil {
			return nil, fmt.Errorf("count pending run requests: %w", err)
		}
		if n >= MaxPendingPerToken {
			return nil, ErrTooManyPending
		}
	}
	start, err := s.syncer.SyncFirst(ctx, r.OrgID, r.TemplateID, r.RequestedByUser)
	if err != nil || start == nil {
		return nil, err
	}
	job := start.JobID
	r.Status, r.ExpiresAt, r.SyncJobID = StatusWaitingForSync, time.Time{}, &job
	return start, nil
}

// endWaitAtCreate moves a request just stored waiting when its sync has
// already finished (it attached to a sync that ended before the request
// was stored, whose finish therefore did not see it). A failure is left to
// the sweep.
func (s *Service) endWaitAtCreate(ctx context.Context, r *Request) {
	rows, err := s.store.ListWaitingFor(ctx, *r.SyncJobID)
	if err == nil {
		err = s.endWaits(ctx, rows)
	}
	if err != nil {
		slog.ErrorContext(ctx, "run request: look at a finished sync", "err", err, "request_id", r.ID)
		return
	}
	got, err := s.store.Get(ctx, r.OrgID, r.ID)
	if err != nil {
		slog.ErrorContext(ctx, "run request: read back after its sync", "err", err, "request_id", r.ID)
		return
	}
	r.Status, r.ExpiresAt, r.SyncError = got.Status, got.ExpiresAt, got.SyncError
}

// SyncFinished moves the requests waiting for the sync job jobID once it
// has finished (jobs.SyncWaiters): the queue calls it inside the finish's
// transaction, so the requests move with the job's terminal state or not
// at all.
func (s *Service) SyncFinished(ctx context.Context, _ uuid.UUID, jobID uuid.UUID) error {
	rows, err := s.store.ListWaitingFor(ctx, jobID)
	if err != nil {
		return err
	}
	return s.endWaits(ctx, rows)
}

// Sweep moves every waiting request whose sync has ended, is gone, or has
// run past MaxSyncWait (the store lists only those, so requests whose sync
// still runs in time never crowd them out). It is the catch-up for what SyncFinished does not
// see: a queued sync cancelled outright, a sync that ended before its
// request was stored, a manager that stopped in between. Run without an
// organization scope it covers every organization; several managers
// sweeping at once move each request once (EndWait only moves a waiting
// one).
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.store.ListDueWaiting(ctx, s.now().Add(-MaxSyncWait))
	if err != nil {
		return err
	}
	return s.endWaits(ctx, rows)
}

// RunSweeper sweeps every interval until ctx ends, logging failures.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.sweepSafely(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) sweepSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "run request sweep panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
		slog.ErrorContext(ctx, "run request sweep failed", "err", err)
	}
}

// endWaits ends the waits rows allow to end now.
func (s *Service) endWaits(ctx context.Context, rows []Waiting) error {
	now := s.now()
	var errs []error
	for _, w := range rows {
		to, expires, reason, ok := endOfWait(w, now)
		if !ok {
			continue
		}
		if _, err := s.store.EndWait(ctx, w.OrgID, w.ID, to, expires, reason); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// endOfWait is what a waiting request becomes at now: pending with its
// expiry starting now when its sync succeeded; failed with the reason when
// the sync failed, was cancelled, is gone or has run past MaxSyncWait; ok
// false while the sync is still queued or running within it.
func endOfWait(w Waiting, now time.Time) (to Status, expiresAt time.Time, reason string, ok bool) {
	switch w.SyncStatus {
	case jobs.StatusSucceeded:
		ttl, err := expiresIn(w.ExpiresInMinutes)
		if err != nil {
			ttl = TTL
		}
		return StatusPending, now.Add(ttl), "", true
	case jobs.StatusFailed, jobs.StatusError:
		if w.SyncError == "" {
			return StatusFailed, now, syncFailedReason, true
		}
		return StatusFailed, now, syncFailedReason + ": " + w.SyncError, true
	case jobs.StatusCancelled:
		return StatusFailed, now, syncCancelledReason, true
	case "":
		return StatusFailed, now, syncGoneReason, true
	}
	if !now.Before(w.CreatedAt.Add(MaxSyncWait)) {
		return StatusFailed, now, syncTimeoutReason, true
	}
	return "", time.Time{}, "", false
}
