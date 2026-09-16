// The daily connection re-test (spec §5, plan "Daily re-test"): a
// background pass over every organization, enqueuing one connection_test
// job per managed host that has no test in the last 24 hours and none
// still queued. The pass runs under an advisory lock — one holder, like
// the migration lock — so a second manager instance's pass skips instead
// of racing it, and each host's job is staggered by
// hash(host_id) % interval, so a fleet's re-tests spread across the pass
// instead of hitting the queue in one burst.
//
// There is no v0.1 scheduler to share a lock key with (checked against the
// codebase at implementation time — the only advisory lock so far is the
// migration's), so the retest carries its own named key, documented here
// for every future background pass to reuse or sit next to.
package schedule

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
)

// Defaults and the advisory lock key.
const (
	// DefaultInterval is the retest pass's period: one pass per hour, each
	// pass enqueueing the hosts that became due since the last window
	// (DefaultWindow ago).
	DefaultInterval = time.Hour
	// RetestWindow is how fresh a host's latest connection test must be
	// for the host to count as tested: 24 hours.
	RetestWindow = 24 * time.Hour
	// AdvisoryKeyRetest is the pg_try_advisory_lock key of the retest
	// pass. Arbitrary but must never change (like db.migrationLockKey); a
	// background pass that wants its own lock picks another constant.
	AdvisoryKeyRetest = int64(8311)
)

// Store is the retest pass's narrow read port: the organizations to
// iterate and the hosts that are due in one of them.
type Store interface {
	ListOrgIDs(ctx context.Context) ([]uuid.UUID, error)
	HostsDueForRetest(ctx context.Context, orgID uuid.UUID, since time.Time) ([]hosts.Host, error)
}

// TestEnqueuer is the enqueue port the pass writes through — jobs.Service's
// EnqueueHostJob, with the connection_test kind and the system trigger.
type TestEnqueuer interface {
	EnqueueHostJob(ctx context.Context, orgID uuid.UUID, kind jobs.Kind, hostIDs []uuid.UUID, payload json.RawMessage, trigger string, actor *uuid.UUID) (jobs.Job, error)
}

// Locker is the advisory lock one pass holds. TryLock is all-or-nothing: a
// pass that loses the race skips its tick — another instance is already
// running it.
type Locker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// Retest is the daily connection re-test scheduler.
type Retest struct {
	store    Store
	enqueuer TestEnqueuer
	locker   Locker
	// Interval is the pass period (DefaultInterval when zero). It is also
	// the stagger span: a host's job is delayed by hash(host_id) % Interval
	// from the pass start.
	interval time.Duration
	// sleep is the interruptible wait (tests fake it; Run returns on its
	// cancellation error).
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
	log   *slog.Logger
}

// NewRetest wires the pass. A nil locker means no lock (single instance,
// tests) — production always passes one.
func NewRetest(store Store, enqueuer TestEnqueuer, locker Locker, interval time.Duration, sleep func(ctx context.Context, d time.Duration) error, now func() time.Time, log *slog.Logger) *Retest {
	return &Retest{store: store, enqueuer: enqueuer, locker: locker, interval: interval, sleep: sleep, now: now, log: log}
}

// Run drives the pass until its context is cancelled: tick, wait the
// interval, repeat. A failing pass is logged, never fatal — the next tick
// retries.
func (r *Retest) Run(ctx context.Context) {
	for {
		if err := r.Tick(ctx); err != nil {
			r.logger().ErrorContext(ctx, "connection re-test pass failed", "err", err)
		}
		if err := r.sleepFor(ctx, r.intervalValue()); err != nil {
			return // the context is cancelled: the clean shutdown
		}
	}
}

// Tick runs one pass: acquire the lock (skip when another instance holds
// it), then for every organization enqueue one staggered connection_test
// per due host. The stagger sleeps are real waits, so the pass takes at
// most one interval and each host's enqueue lands at its own hash slot.
func (r *Retest) Tick(ctx context.Context) error {
	if r.locker != nil {
		locked, err := r.locker.TryLock(ctx)
		if err != nil {
			return fmt.Errorf("acquire retest lock: %w", err)
		}
		if !locked {
			return nil // another instance's pass is running
		}
		defer r.locker.Unlock(ctx)
	}
	orgs, err := r.store.ListOrgIDs(ctx)
	if err != nil {
		return fmt.Errorf("list organizations: %w", err)
	}
	since := r.nowValue().Add(-RetestWindow)
	for _, org := range orgs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		due, err := r.store.HostsDueForRetest(ctx, org, since)
		if err != nil {
			// One broken organization does not stop the rest: the pass
			// logs and moves on.
			r.logger().ErrorContext(ctx, "connection re-test: select due hosts", "org", org, "err", err)
			continue
		}
		if err := r.enqueueStaggered(ctx, org, due); err != nil {
			return err
		}
	}
	return nil
}

// enqueueStaggered enqueues one connection_test per host, staggered by
// hash(host_id) % interval: the hosts sorted by their delay, each sleeping
// the delta to its predecessor, so the waits add up to less than one
// interval no matter how many hosts are due.
func (r *Retest) enqueueStaggered(ctx context.Context, org uuid.UUID, due []hosts.Host) error {
	if len(due) == 0 {
		return nil
	}
	ordered := make([]hosts.Host, len(due))
	copy(ordered, due)
	slices.SortFunc(ordered, func(a, b hosts.Host) int {
		return cmp.Compare(DelayOf(a.ID, r.intervalValue()), DelayOf(b.ID, r.intervalValue()))
	})
	var last time.Duration
	for i, h := range ordered {
		d := DelayOf(h.ID, r.intervalValue())
		wait := d
		if i > 0 {
			wait = d - last
		}
		if err := r.sleepFor(ctx, wait); err != nil {
			return err
		}
		last = d
		if _, err := r.enqueuer.EnqueueHostJob(ctx, org, jobs.KindConnectionTest, []uuid.UUID{h.ID}, nil, jobs.TriggerSystem, nil); err != nil {
			// One host's enqueue failure does not stop the others; the
			// host stays due and the next pass picks it up.
			r.logger().ErrorContext(ctx, "connection re-test: enqueue", "org", org, "host", h.ID, "err", err)
		}
	}
	return nil
}

// DelayOf hashes the host id into the stagger span: the first 8 bytes of
// SHA-256 over the UUID's string form, as a big-endian uint64 modulo the
// interval. Deterministic — every pass staggers a given host the same way,
// so re-tests never all fire at once after a manager restart either.
func DelayOf(hostID uuid.UUID, interval time.Duration) time.Duration {
	sum := sha256.Sum256([]byte(hostID.String()))
	slot := binary.BigEndian.Uint64(sum[:8])
	return time.Duration(slot % uint64(interval))
}

func (r *Retest) intervalValue() time.Duration {
	if r.interval > 0 {
		return r.interval
	}
	return DefaultInterval
}

func (r *Retest) nowValue() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Retest) sleepFor(ctx context.Context, d time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (r *Retest) logger() *slog.Logger {
	if r.log != nil {
		return r.log
	}
	return slog.Default()
}

// AdvisoryLocker is the Locker production wiring uses: a session-scoped
// pg_try_advisory_lock, acquired and released on one dedicated pooled
// connection, because advisory locks live on the session, not the
// statement.
type AdvisoryLocker struct {
	pool *pgxpool.Pool
	key  int64
	conn *pgxpool.Conn
}

// NewAdvisoryLocker builds the locker for the pool and a named key.
func NewAdvisoryLocker(pool *pgxpool.Pool, key int64) *AdvisoryLocker {
	return &AdvisoryLocker{pool: pool, key: key}
}

// TryLock acquires the advisory lock on a dedicated session, held until
// Unlock. The connection is released (and with it the session lock) there.
func (l *AdvisoryLocker) TryLock(ctx context.Context) (bool, error) {
	if l.conn != nil {
		return true, nil // already holding this instance's lock
	}
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire session: %w", err)
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&locked); err != nil {
		conn.Release()
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !locked {
		conn.Release()
		return false, nil
	}
	l.conn = conn
	return true, nil
}

// Unlock releases the advisory lock and its dedicated session. A locker
// that holds nothing is a no-op. The unlock runs on a context detached from
// ctx's cancellation (the scheduler hands its tick context, which shutdown
// cancels): a cancelled unlock would return the pooled session to the pool
// still holding the lock, locking every other holder out until that
// session happens to close.
func (l *AdvisoryLocker) Unlock(ctx context.Context) {
	if l.conn == nil {
		return
	}
	conn := l.conn
	l.conn = nil
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, l.key); err != nil {
		// The session itself is about to go back to the pool; a failed
		// unlock there leaves the lock held until the session closes —
		// logged so an operator sees the gap.
		slog.ErrorContext(ctx, "release retest advisory lock", "err", err)
	}
	conn.Release()
}
