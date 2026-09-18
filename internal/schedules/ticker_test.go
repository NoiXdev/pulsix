package schedules_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/schedules"
)

type fakeDue struct {
	mu        sync.Mutex
	due       []schedules.Schedule
	decisions []schedules.Decision
	nowSeen   []time.Time
	limitSeen int
	err       error
}

func (f *fakeDue) ProcessDue(ctx context.Context, now time.Time, limit int, decide func(context.Context, schedules.Schedule) schedules.Decision) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nowSeen, f.limitSeen = append(f.nowSeen, now), limit
	if f.err != nil {
		return 0, f.err
	}
	for _, s := range f.due {
		f.decisions = append(f.decisions, decide(ctx, s))
	}
	n := len(f.due)
	f.due = nil
	return n, nil
}

func TestTickerTickUsesTheInjectedClockAndGrace(t *testing.T) {
	w := newWorld(t)
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	s := due(t, w, "0 * * * *", base, schedules.Guards{})
	store := &fakeDue{due: []schedules.Schedule{s}}
	now := base.Add(40 * time.Second)
	tk := schedules.NewTicker(w.svc, store, 15*time.Second, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := tk.Tick(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	// 40 s late with a 15 s tick exceeds the 30 s grace: missed.
	if store.decisions[0].Run.Outcome != schedules.OutcomeMissed || !store.nowSeen[0].Equal(now) || store.limitSeen != 50 {
		t.Fatalf("decision = %+v, now %v, limit %d", store.decisions[0], store.nowSeen, store.limitSeen)
	}
}

func TestTickerRunStopsOnCancel(t *testing.T) {
	w := newWorld(t)
	store := &fakeDue{err: errors.New("db down")}
	ctx, cancel := context.WithCancel(context.Background())
	ticks := 0
	sleep := func(ctx context.Context, _ time.Duration) error {
		ticks++
		if ticks == 3 {
			cancel()
		}
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		schedules.NewTicker(w.svc, store, time.Second, time.Now, sleep, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if len(store.nowSeen) != 3 {
		t.Fatalf("ticks = %d, want 3 (errors keep the loop alive)", len(store.nowSeen))
	}
}
