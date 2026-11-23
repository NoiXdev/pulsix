// The invite reaper (backlog 3.128): an invitation nobody accepts stays in
// `invites` for good. Since 3.125 the list asked for with the expired ones
// shows only those that lapsed within ExpiredInviteListWindow, so older
// ones are out of sight but not out of the table, and their link hashes
// pile up. This sweep deletes the unaccepted invitations that expired
// longer ago than that window — exactly the ones no list shows any more,
// and which can no longer be resent. Accepted invitations are history and
// stay.
//
// Same shape as the auth reaper (internal/auth/reaper.go): a ticker with an
// injected interval (default 1 h), a constructor-injected clock, one sweep
// per tick, and a session-scoped advisory lock — key AdvisoryKeyInviteReaper,
// a fresh named key in the same hand-picked series — so a second manager
// instance's sweep skips instead of racing the first. The delete runs
// org-unscoped: with no pulsix.org_id bound, the invites table's
// org_isolation_bypass policy lets it see every organization's rows.
package tenancy

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	// DefaultInviteReaperInterval is the sweep period: one pass per hour.
	// The threshold is days, so an hourly pass is far more than timely.
	DefaultInviteReaperInterval = time.Hour
	// AdvisoryKeyInviteReaper is the pg_try_advisory_lock key of this
	// reaper. Arbitrary but must never change (like db.migrationLockKey
	// 7256, schedule.AdvisoryKeyRetest 8311 and auth.AdvisoryKeyAuthReaper
	// 8312, which this sits next to in the same hand-picked series); a
	// background pass that wants its own lock picks another fresh constant.
	AdvisoryKeyInviteReaper = int64(8318)
)

// InviteReaperStore is the reaper's narrow write port: one org-unscoped
// delete.
type InviteReaperStore interface {
	// DeleteStaleInvites removes every unaccepted invitation whose
	// expires_at is before expiredBefore, in every organization, and
	// reports how many went. Accepted invitations are never touched.
	DeleteStaleInvites(ctx context.Context, expiredBefore time.Time) (int64, error)
}

// InviteReaperLocker is the advisory-lock port (schedule.AdvisoryLocker
// satisfies it): the sweep holds the session lock across its delete so
// concurrent manager instances do not double-run. A nil locker skips
// locking entirely — single-instance deployments and tests.
type InviteReaperLocker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// InviteReaper purges long-expired, unaccepted invitations on an interval.
type InviteReaper struct {
	store    InviteReaperStore
	locker   InviteReaperLocker
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
}

// NewInviteReaper wires the reaper. interval <= 0 keeps
// DefaultInviteReaperInterval; a nil clock degrades to time.Now; a nil
// locker skips the advisory lock; a nil logger degrades to slog.Default.
func NewInviteReaper(store InviteReaperStore, locker InviteReaperLocker, interval time.Duration, now func() time.Time, log *slog.Logger) *InviteReaper {
	return &InviteReaper{store: store, locker: locker, interval: interval, now: now, log: log}
}

// Run sweeps every interval until ctx is done. A failing sweep is logged,
// never fatal — the next tick retries.
func (r *InviteReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.intervalValue())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.Sweep(ctx); err != nil {
				r.logger().ErrorContext(ctx, "invite reaper sweep failed", "err", err)
			}
		}
	}
}

// Sweep does one pass and reports how many invitations it removed. The
// advisory lock (when wired) is held across the delete, so a concurrent
// instance's sweep skips and reports zero. The cutoff is the sweep's now
// minus ExpiredInviteListWindow, so only invitations the expired list no
// longer shows are removed.
func (r *InviteReaper) Sweep(ctx context.Context) (int64, error) {
	if r.locker != nil {
		locked, err := r.locker.TryLock(ctx)
		if err != nil {
			return 0, fmt.Errorf("acquire invite reaper lock: %w", err)
		}
		if !locked {
			return 0, nil // another instance's sweep is running
		}
		defer r.locker.Unlock(ctx)
	}
	n, err := r.store.DeleteStaleInvites(ctx, r.nowValue().Add(-ExpiredInviteListWindow))
	if err != nil {
		return 0, fmt.Errorf("delete stale invites: %w", err)
	}
	return n, nil
}

func (r *InviteReaper) intervalValue() time.Duration {
	if r.interval > 0 {
		return r.interval
	}
	return DefaultInviteReaperInterval
}

func (r *InviteReaper) nowValue() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *InviteReaper) logger() *slog.Logger {
	if r.log != nil {
		return r.log
	}
	return slog.Default()
}
