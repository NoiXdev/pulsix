package schedules

import (
	"context"
	"log/slog"
	"time"
)

// DefaultTick is the ticker interval when none is configured; batchSize
// bounds one transaction; keepRuns is the per-schedule run retention.
const (
	DefaultTick = 15 * time.Second
	batchSize   = 50
	keepRuns    = 500
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
		if _, err := t.Tick(ctx); err != nil {
			t.log.ErrorContext(ctx, "schedule tick failed", "err", err)
		}
		if err := t.sleep(ctx, t.tick); err != nil {
			return
		}
	}
}

// Tick decides every due schedule, one locked batch at a time, until a
// batch comes back smaller than batchSize.
func (t *Ticker) Tick(ctx context.Context) (int, error) {
	total := 0
	for {
		now := t.now()
		n, err := t.store.ProcessDue(ctx, now, batchSize, func(ctx context.Context, s Schedule) Decision {
			return t.svc.Decide(ctx, s, now, 2*t.tick)
		})
		total += n
		if err != nil || n < batchSize {
			return total, err
		}
	}
}
