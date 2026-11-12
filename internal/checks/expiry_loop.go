package checks

// The hourly expiry loop (backlog 3.12b, spec "Automatic expiry"): once per
// interval one manager forgets the items that have been silent for the
// configured period while their host kept reporting (Service.ForgetExpired).
// Like the connection re-test and the auth reaper it holds a session-scoped
// advisory lock for the round, so a second manager instance skips instead
// of racing it; the key is ExpiryAdvisoryKey, next to schedule's 8311 and
// the auth reaper's 8312 (8313 and 8314 are taken by the probe runner and
// the follower).

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultExpiryInterval is the period between rounds.
	DefaultExpiryInterval = time.Hour
	// ExpiryRoundLimit bounds the items one round forgets; the rest wait
	// for the next round. ForgetExpired is called once per round with the
	// whole limit, since every call rescans check_current.
	ExpiryRoundLimit = 5000
	// ExpiryRoundTimeout bounds one round. A forget waits on its host's
	// evaluation-cursor lock without a lock_timeout, so a cursor held
	// indefinitely must not stall the loop for good.
	ExpiryRoundTimeout = 10 * time.Minute
	// ExpiryAdvisoryKey is the pg_try_advisory_lock key of the expiry loop.
	// Arbitrary but must never change.
	ExpiryAdvisoryKey = int64(8315)
)

// ExpiryForgetter is the loop's one dependency: *Service.
type ExpiryForgetter interface {
	ForgetExpired(ctx context.Context, period time.Duration, limit int) (map[uuid.UUID]int, error)
}

// ExpiryLocker is the advisory-lock port (schedule.AdvisoryLocker satisfies
// it). A round that does not get the lock is skipped.
type ExpiryLocker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// ExpiryLoop forgets quiet items on an interval.
type ExpiryLoop struct {
	forgetter ExpiryForgetter
	locker    ExpiryLocker
	period    time.Duration
	interval  time.Duration
	log       *slog.Logger
}

// NewExpiryLoop wires the loop. period <= 0 disables it (Run returns at
// once); interval <= 0 keeps DefaultExpiryInterval; a nil logger degrades to
// slog.Default.
func NewExpiryLoop(f ExpiryForgetter, l ExpiryLocker, period, interval time.Duration, log *slog.Logger) *ExpiryLoop {
	if interval <= 0 {
		interval = DefaultExpiryInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &ExpiryLoop{forgetter: f, locker: l, period: period, interval: interval, log: log}
}

// Run does a round every interval until ctx is done. A failing round is
// logged, never fatal.
func (l *ExpiryLoop) Run(ctx context.Context) {
	if l.period <= 0 {
		return
	}
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Round(ctx)
		}
	}
}

// Round runs one pass: take the lock (skip when another instance holds it),
// forget up to ExpiryRoundLimit quiet items under a deadline, log one INFO
// line with the per-organization counts. A panic is recovered and logged
// with its stack so it cannot take the manager down.
func (l *ExpiryLoop) Round(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			l.log.ErrorContext(ctx, "item expiry round panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if l.locker != nil {
		locked, err := l.locker.TryLock(ctx)
		if err != nil {
			l.log.ErrorContext(ctx, "item expiry: acquire lock", "err", err)
			return
		}
		if !locked {
			return // another instance's round is running
		}
		defer l.locker.Unlock(ctx)
	}
	rctx, cancel := context.WithTimeout(ctx, ExpiryRoundTimeout)
	defer cancel()
	counts, err := l.forgetter.ForgetExpired(rctx, l.period, ExpiryRoundLimit)
	if err != nil {
		l.log.ErrorContext(ctx, "item expiry round failed", "err", err, "forgotten", orgCounts(counts))
		return
	}
	l.log.InfoContext(ctx, "item expiry round", "forgotten", orgCounts(counts))
}

// orgCounts renders the per-organization counts as a loggable map.
func orgCounts(counts map[uuid.UUID]int) map[string]int {
	out := make(map[string]int, len(counts))
	for org, n := range counts {
		out[org.String()] = n
	}
	return out
}
