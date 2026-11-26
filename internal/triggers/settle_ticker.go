package triggers

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// DefaultSettleTick is how often the settle ticker looks for due pending
// runs when no tick is configured: a collected run starts at most this
// long after its moment. settleBatchSize bounds one transaction;
// settleBatchDeadlineFactor and minSettleBatchDeadline bound one batch as
// the schedule ticker bounds its own (a starved pool fails fast instead of
// holding the batch's row locks).
const (
	DefaultSettleTick         = 5 * time.Second
	settleBatchSize           = 50
	settleBatchDeadlineFactor = 4
	minSettleBatchDeadline    = 10 * time.Second
)

// SettleTicker starts the collected runs of triggers with a settle delay
// once they are due (backlog 2.137). Several managers may run one each:
// the claim's SKIP LOCKED hands every pending run to one of them.
type SettleTicker struct {
	svc   *Service
	tick  time.Duration
	sleep func(ctx context.Context, d time.Duration) error
	log   *slog.Logger
}

// NewSettleTicker builds the ticker over svc's pending store. It reads
// svc's clock (Deps.Now), the one the receive path stamps deliveries and
// due times with, so the two never disagree. A zero tick is
// DefaultSettleTick, nil sleep waits on the wall clock, nil log is
// slog.Default.
func NewSettleTicker(svc *Service, tick time.Duration, sleep func(ctx context.Context, d time.Duration) error, log *slog.Logger) *SettleTicker {
	if tick <= 0 {
		tick = DefaultSettleTick
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &SettleTicker{svc: svc, tick: tick, sleep: sleep, log: log}
}

// Run ticks until ctx is cancelled; a failed tick is logged and retried.
func (t *SettleTicker) Run(ctx context.Context) {
	for {
		t.tickSafely(ctx)
		if err := t.sleep(ctx, t.tick); err != nil {
			return
		}
	}
}

func (t *SettleTicker) tickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.log.ErrorContext(ctx, "settle tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if _, err := t.Tick(ctx); err != nil && ctx.Err() == nil {
		t.log.ErrorContext(ctx, "settle tick failed", "err", err)
	}
}

// Tick starts, drops or fails every claimable pending run, each in its own
// transaction (so a delivery never waits behind other triggers' starts),
// in passes of up to settleBatchSize under one deadline, until a pass
// applies fewer — a pending run that waits for an active run is never
// claimed, so it cannot keep this loop going. It is the ticker's step hook: tests call it with a
// fake clock instead of waiting.
func (t *SettleTicker) Tick(ctx context.Context) (int, error) {
	if t.svc.d.Pending == nil {
		return 0, nil
	}
	total := 0
	for {
		batchCtx, cancel := context.WithTimeout(ctx, max(settleBatchDeadlineFactor*t.tick, minSettleBatchDeadline))
		n, err := t.svc.d.Pending.ProcessDue(batchCtx, t.svc.d.Now(), settleBatchSize, t.svc.fireTicked)
		cancel()
		total += n
		if err != nil || n < settleBatchSize {
			return total, err
		}
	}
}
