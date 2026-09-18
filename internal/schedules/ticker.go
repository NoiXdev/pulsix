package schedules

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// DefaultTick is the ticker interval when none is configured; batchSize
// bounds one transaction; keepRuns is the per-schedule run retention.
// batchDeadlineFactor bounds one ProcessDue call (one batch) to a small
// multiple of the tick: long enough for a healthy batch of batchSize
// schedules, short enough that a starved pool connection — the v0.2b
// pool-exhaustion shape, fix round 1 Important #3 — fails fast instead of
// holding the batch's row locks, and the pool connection an HTTP handler
// is waiting on, forever.
const (
	DefaultTick         = 15 * time.Second
	batchSize           = 50
	keepRuns            = 500
	batchDeadlineFactor = 4
)

// DueStore locks due schedules and writes back one decision each.
type DueStore interface {
	ProcessDue(ctx context.Context, now time.Time, limit int, decide func(ctx context.Context, s Schedule) Decision) (int, error)
}

// Ticker fires due schedules. Several managers may run one each.
type Ticker struct {
	svc   *Service
	store DueStore
	tick  time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	log   *slog.Logger
}

// NewTicker builds the ticker; nil sleep waits on the wall clock, nil log
// is slog.Default, a zero tick is DefaultTick.
func NewTicker(svc *Service, store DueStore, tick time.Duration, now func() time.Time, sleep func(ctx context.Context, d time.Duration) error, log *slog.Logger) *Ticker {
	if tick <= 0 {
		tick = DefaultTick
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Ticker{svc: svc, store: store, tick: tick, now: now, sleep: sleep, log: log}
}

// Run ticks until ctx is cancelled; a failed tick is logged and retried.
func (t *Ticker) Run(ctx context.Context) {
	for {
		t.tickSafely(ctx)
		if err := t.sleep(ctx, t.tick); err != nil {
			return
		}
	}
}

// tickSafely runs one Tick, recovering any panic that escapes it — a
// defense in depth beyond Decide's own per-schedule recover (fix round 1,
// Important #1's "also recover at the loop level"): a bug anywhere else in
// the DueStore's row loop must not take the whole ticker goroutine, and so
// the whole manager process, down with it. The tick is simply skipped; the
// next one, batchSize schedules later, retries. A clean shutdown surfaces
// as ctx.Err() on the tick's own error, not a failure worth logging at
// ERROR on every stop.
func (t *Ticker) tickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.log.ErrorContext(ctx, "schedule tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if _, err := t.Tick(ctx); err != nil && ctx.Err() == nil {
		t.log.ErrorContext(ctx, "schedule tick failed", "err", err)
	}
}

// Tick decides every due schedule, one locked batch at a time, until a
// batch comes back smaller than batchSize. Each batch's own context is
// bounded to batchDeadlineFactor × tick (fix round 1, Important #3): a
// batch that cannot complete in that time — most likely a starved pool
// connection — fails fast instead of holding its row locks (and the
// connection a blocked HTTP handler needs) indefinitely; the same rows
// are still due on the next tick, the same at-least-once guarantee a
// failed commit already gives.
func (t *Ticker) Tick(ctx context.Context) (int, error) {
	total := 0
	for {
		now := t.now()
		batchCtx, cancel := context.WithTimeout(ctx, batchDeadlineFactor*t.tick)
		n, err := t.store.ProcessDue(batchCtx, now, batchSize, func(ctx context.Context, s Schedule) Decision {
			return t.svc.Decide(ctx, s, now, 2*t.tick)
		})
		cancel()
		total += n
		if err != nil || n < batchSize {
			return total, err
		}
	}
}
