// The auth reaper (backlog 1.21, v0.1 release blocker): bounds the growth
// of the two unauthenticated/expired write paths. `webauthn_ceremonies`
// rows are written by BeginLogin/BeginRegistration before any
// authentication exists (a login ceremony has no user id at all), and a
// ceremony that is never finished is never touched again — Take only
// deletes the row whose id is presented, which an abandoned ceremony's
// never is. `sessions` rows outlive every 12-hour expiry and every logout
// (a revoke only stamps revoked_at). Both tables grow without bound
// without this sweep — the blocker's whole point.
//
// Deleting an expired session is safe: GetSession (the login lookup's own
// query) never resolves a row with expires_at <= now(), so an expired
// session is already unauthenticated at read time — the reaper never
// invalidates a credential anyone could still use; it only bounds growth.
// The 7-day retention grace keeps recently expired (and revoked) sessions
// around for incident forensics (an operator auditing "who was logged in
// on Monday evening") before they are purged, and it spreads the deletions
// over a week instead of one burst at expiry.
//
// Same shape as the v0.2b job reaper (internal/jobs/reaper.go): a ticker
// with an injected interval (default 1 h), a constructor-injected clock,
// one sweep per tick. Like the connection re-test's pass
// (internal/schedule/retest.go) it holds a session-scoped advisory lock —
// key AdvisoryKeyAuthReaper, a fresh named key next to the existing ones
// (db.migrationLockKey 7256, schedule.AdvisoryKeyRetest 8311, assigned
// once and never changed) — so a second manager instance's sweep skips
// instead of racing the first over the same rows.
package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Reaper windows and the advisory lock key.
const (
	// DefaultReaperInterval is the sweep period: one pass per hour. The
	// thresholds are days, so an hourly pass is far more than timely — and
	// matches the plan's default (and the retest scheduler's).
	DefaultReaperInterval = time.Hour
	// SessionRetentionGrace is how long past its expiry a session row may
	// still be deleted: expired sessions are unauthenticated at read time
	// (GetSession refuses them), so the reaper only bounds growth; the 7
	// days are a retention window for forensics, not a security margin.
	SessionRetentionGrace = 7 * 24 * time.Hour
	// AdvisoryKeyAuthReaper is the pg_try_advisory_lock key of this reaper.
	// Arbitrary but must never change (like db.migrationLockKey 7256 and
	// schedule.AdvisoryKeyRetest 8311, which this sits next to in the same
	// hand-picked series); a background pass that wants its own lock picks
	// another fresh constant.
	AdvisoryKeyAuthReaper = int64(8312)
)

// ReaperStore is the reaper's narrow write port: the two expired-row
// deletes, org-unscoped (sessions and ceremonies are user/instance rows,
// not org rows).
type ReaperStore interface {
	// DeleteExpiredCeremonies removes every ceremony whose expires_at is
	// past expiredBefore (the sweep's now) and reports how many went.
	DeleteExpiredCeremonies(ctx context.Context, expiredBefore time.Time) (int64, error)
	// DeleteExpiredSessions removes every session whose expires_at is past
	// expiredBefore (the sweep's now minus SessionRetentionGrace) and
	// reports how many went.
	DeleteExpiredSessions(ctx context.Context, expiredBefore time.Time) (int64, error)
}

// Locker is the advisory-lock port (schedule.AdvisoryLocker satisfies it):
// the sweep holds the session lock across its deletes so concurrent
// manager instances do not double-run. A nil locker skips locking
// entirely — single-instance deployments and tests.
type Locker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// Reaper sweeps expired ceremonies and sessions on an interval.
type Reaper struct {
	store    ReaperStore
	locker   Locker
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
}

// NewReaper wires the reaper. interval <= 0 keeps DefaultReaperInterval; a
// nil clock degrades to time.Now; a nil locker skips the advisory lock; a
// nil logger degrades to slog.Default.
func NewReaper(store ReaperStore, locker Locker, interval time.Duration, now func() time.Time, log *slog.Logger) *Reaper {
	return &Reaper{store: store, locker: locker, interval: interval, now: now, log: log}
}

// Run sweeps every interval until ctx is done. A failing sweep is logged,
// never fatal — the next tick retries.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.intervalValue())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, err := r.Sweep(ctx); err != nil {
				r.logger().ErrorContext(ctx, "auth reaper sweep failed", "err", err)
			}
		}
	}
}

// Sweep does one pass and returns how many ceremonies and sessions were
// deleted. The advisory lock (when wired) is held across both deletes, so
// a concurrent instance's sweep skips wholesale; the ceremonies' delete
// runs first and a failing session delete still reports the ceremonies
// count (the first error stops nothing else — each delete is its own
// statement and the next tick retries whatever remains).
func (r *Reaper) Sweep(ctx context.Context) (ceremonies, sessions int64, err error) {
	if r.locker != nil {
		locked, err := r.locker.TryLock(ctx)
		if err != nil {
			return 0, 0, fmt.Errorf("acquire auth reaper lock: %w", err)
		}
		if !locked {
			return 0, 0, nil // another instance's sweep is running
		}
		defer r.locker.Unlock(ctx)
	}

	now := r.nowValue()
	ceremonies, err = r.store.DeleteExpiredCeremonies(ctx, now)
	if err != nil {
		return 0, 0, fmt.Errorf("delete expired ceremonies: %w", err)
	}
	sessions, err = r.store.DeleteExpiredSessions(ctx, now.Add(-SessionRetentionGrace))
	if err != nil {
		return ceremonies, 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return ceremonies, sessions, nil
}

func (r *Reaper) intervalValue() time.Duration {
	if r.interval > 0 {
		return r.interval
	}
	return DefaultReaperInterval
}

func (r *Reaper) nowValue() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Reaper) logger() *slog.Logger {
	if r.log != nil {
		return r.log
	}
	return slog.Default()
}
