package checks

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeTicker is a channel-driven ticker the test controls; the channel
// is buffered so a test can push a tick without a listener necessarily
// being ready yet (a stopped or not-yet-started check's channel is
// never read again, and a blocking push there would hang the test).
type fakeTicker struct {
	c       chan time.Time
	mu      sync.Mutex
	stopped bool
}

func newFakeTicker() *fakeTicker { return &fakeTicker{c: make(chan time.Time, 4)} }

func (f *fakeTicker) C() <-chan time.Time { return f.c }

func (f *fakeTicker) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *fakeTicker) wasStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// fakeTickers hands out fakeTickers, recording their intervals and the
// tickers themselves in hand-out order — the same convention
// internal/agent's own loop_fakes_test.go uses.
type fakeTickers struct {
	mu        sync.Mutex
	intervals []time.Duration
	tickers   []*fakeTicker
}

func (f *fakeTickers) factory() func(time.Duration) Ticker {
	return func(d time.Duration) Ticker {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.intervals = append(f.intervals, d)
		ft := newFakeTicker()
		f.tickers = append(f.tickers, ft)
		return ft
	}
}

func (f *fakeTickers) recorded() ([]time.Duration, []*fakeTicker) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.intervals...), append([]*fakeTicker(nil), f.tickers...)
}

// fakePlugin is a controllable Plugin: every call returns the currently
// scripted observations/error and records the params it was called with.
type fakePlugin struct {
	mu        sync.Mutex
	calls     int
	obs       []Observation
	err       error
	lastCtx   context.Context
	lastParam map[string]any
}

func (p *fakePlugin) Name() string { return "fake" }

func (p *fakePlugin) Collect(ctx context.Context, params map[string]any) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastCtx = ctx
	p.lastParam = params
	return p.obs, p.err
}

func (p *fakePlugin) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeSink records every batch handed to it, safe for concurrent
// scheduler goroutines to call.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]Result
}

func (s *fakeSink) collect(r []Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, r)
}

func (s *fakeSink) all() [][]Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]Result(nil), s.batches...)
}

func waitForSchedulerTest(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestSchedulerFiresCheckAtItsInterval(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Item: "", Metric: "cpu.used_pct", Value: floatPtr(42)}}}
	reg := map[string]Plugin{"cpu": plugin}
	ticks := &fakeTickers{}
	fixedNow := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	sched := NewScheduler(reg, func() time.Time { return fixedNow }, ticks.factory())

	sched.Apply(Config{Version: "v1", Checks: []Spec{
		{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second},
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	if len(tickers) != 1 {
		t.Fatalf("tickers created = %d, want 1", len(tickers))
	}
	tickers[0].c <- fixedNow
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	batch := sink.all()[0]
	if len(batch) != 1 {
		t.Fatalf("results = %d, want 1", len(batch))
	}
	r := batch[0]
	if r.CheckKey != "cpu" || r.Metric != "cpu.used_pct" || r.Value == nil || *r.Value != 42 {
		t.Errorf("result = %+v, want cpu/cpu.used_pct/42", r)
	}
	if !r.At.Equal(fixedNow) {
		t.Errorf("At = %v, want %v (the injected clock, not the ticker's own time)", r.At, fixedNow)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !tickers[0].wasStopped() {
		t.Error("the check's ticker must be stopped when Run's context ends")
	}
}

func TestSchedulerApplyUnchangedConfigKeepsPhase(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	reg := map[string]Plugin{"cpu": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())

	cfg := Config{Version: "v1", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}}}
	sched.Apply(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	if len(tickers) != 1 {
		t.Fatalf("tickers after first apply = %d, want 1", len(tickers))
	}

	// Re-apply the identical configuration: no new ticker, the old one
	// never stopped.
	sched.Apply(cfg)
	_, tickers2 := ticks.recorded()
	if len(tickers2) != 1 {
		t.Fatalf("tickers after re-applying an unchanged config = %d, want still 1", len(tickers2))
	}
	if tickers2[0].wasStopped() {
		t.Error("an unchanged check's ticker must not be stopped by Apply")
	}

	// The original ticker still drives the check: proof the phase is the
	// SAME timer, not a freshly-restarted one.
	tickers2[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	cancel()
	<-done
}

func TestSchedulerApplyChangedIntervalRestartsTimer(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	reg := map[string]Plugin{"cpu": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())

	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	if len(tickers) != 1 {
		t.Fatalf("tickers = %d, want 1", len(tickers))
	}
	oldTicker := tickers[0]

	// A changed interval, same key: the old timer must retire...
	sched.Apply(Config{Version: "v2", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: 45 * time.Second}}})
	waitForSchedulerTest(t, time.Second, oldTicker.wasStopped)

	// ...and a fresh one takes over.
	_, tickers2 := ticks.recorded()
	if len(tickers2) != 2 {
		t.Fatalf("tickers after a changed interval = %d, want 2 (old + new)", len(tickers2))
	}
	if tickers2[1] == oldTicker {
		t.Fatal("the new ticker must not be the same instance as the old one")
	}
	newTicker := tickers2[1]
	newTicker.c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	cancel()
	<-done
}

func TestSchedulerApplyChangedParamsRestartsTimer(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	reg := map[string]Plugin{"disk": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())

	sched.Apply(Config{Version: "v1", Checks: []Spec{
		{Key: "disk", Plugin: "disk", Interval: time.Minute, Params: map[string]any{"mounts": []any{"/"}}},
	}})
	_, tickers := ticks.recorded()
	oldTicker := tickers[0]

	sched.Apply(Config{Version: "v2", Checks: []Spec{
		{Key: "disk", Plugin: "disk", Interval: time.Minute, Params: map[string]any{"mounts": []any{"/", "/data"}}},
	}})

	if !oldTicker.wasStopped() {
		t.Error("a changed params map must retire the old timer even with the same interval")
	}
	_, tickers2 := ticks.recorded()
	if len(tickers2) != 2 {
		t.Fatalf("tickers after a changed params map = %d, want 2", len(tickers2))
	}
}

func TestSchedulerVersionMatchesApplied(t *testing.T) {
	sched := NewScheduler(map[string]Plugin{}, time.Now, (&fakeTickers{}).factory())
	if v := sched.Version(); v != "" {
		t.Errorf("Version before any Apply = %q, want empty", v)
	}
	sched.Apply(Config{Version: "abc123"})
	if v := sched.Version(); v != "abc123" {
		t.Errorf("Version = %q, want abc123", v)
	}
	sched.Apply(Config{Version: "def456"})
	if v := sched.Version(); v != "def456" {
		t.Errorf("Version = %q, want def456", v)
	}
}

func TestSchedulerApplyRemovesCheckNotInConfig(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	reg := map[string]Plugin{"cpu": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())

	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	oldTicker := tickers[0]

	// The check disappears from the next configuration entirely.
	sched.Apply(Config{Version: "v2", Checks: nil})
	waitForSchedulerTest(t, time.Second, oldTicker.wasStopped)

	// Re-adding the SAME key afterward must create a brand new timer —
	// proof the check was fully retired, not merely paused.
	sched.Apply(Config{Version: "v3", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})
	_, tickers2 := ticks.recorded()
	if len(tickers2) != 2 {
		t.Fatalf("tickers after re-adding a removed key = %d, want 2", len(tickers2))
	}

	cancel()
	<-done
}

func TestSchedulerUnknownPluginNeverCollectsOrPanics(t *testing.T) {
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{}, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "ghost", Plugin: "does-not-exist", Interval: time.Minute}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	if len(tickers) != 1 {
		t.Fatalf("tickers = %d, want 1 (the ticker is still created even for an unknown plugin)", len(tickers))
	}
	tickers[0].c <- time.Now()
	time.Sleep(20 * time.Millisecond) // give a (wrongly) spawned goroutine a chance to misbehave
	if len(sink.all()) != 0 {
		t.Error("an unknown plugin must never produce a result")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestSchedulerApplyBeforeRunStartsOnRunBegin(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(7)}}}
	reg := map[string]Plugin{"cpu": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())

	// Apply BEFORE Run is ever called.
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})
	_, tickers := ticks.recorded()
	if len(tickers) != 1 {
		t.Fatalf("tickers after Apply-before-Run = %d, want 1 (created eagerly, started lazily)", len(tickers))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	cancel()
	<-done
}

func TestSchedulerCollectErrorWithNoObservationsProducesNoResult(t *testing.T) {
	plugin := &fakePlugin{err: context.DeadlineExceeded}
	reg := map[string]Plugin{"cpu": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return plugin.callCount() == 1 })
	time.Sleep(20 * time.Millisecond)
	if len(sink.all()) != 0 {
		t.Error("a Collect call with no observations at all must never reach the sink")
	}

	cancel()
	<-done
}

func TestSchedulerPartialObservationsWithErrorStillSent(t *testing.T) {
	plugin := &fakePlugin{
		obs: []Observation{{Metric: "disk.used_pct", Item: "/", Value: floatPtr(10)}},
		err: context.DeadlineExceeded, // advisory: one mount failed, the other still reported
	}
	reg := map[string]Plugin{"disk": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "disk", Plugin: "disk", Interval: time.Minute}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })
	if got := sink.all()[0][0].Item; got != "/" {
		t.Errorf("item = %q, want /", got)
	}

	cancel()
	<-done
}

func TestSchedulerParamsPassedThroughToPlugin(t *testing.T) {
	plugin := &fakePlugin{}
	reg := map[string]Plugin{"disk": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())
	params := map[string]any{"mounts": []any{"/data"}}
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "disk", Plugin: "disk", Interval: time.Minute, Params: params}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, (&fakeSink{}).collect) }()

	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return plugin.callCount() == 1 })

	plugin.mu.Lock()
	got := plugin.lastParam
	plugin.mu.Unlock()
	if got["mounts"] == nil {
		t.Errorf("params passed to plugin = %+v, want mounts present", got)
	}

	cancel()
	<-done
}

// TestSchedulerErrObservationCarriesThroughAsResultErr pins the coordinator's
// named review scenario at the scheduler boundary: one item's Observation
// carries Err (exactly what disk's real Collect produces for an
// unreadable mount alongside the mounts that succeeded), and the
// scheduler's Result for that item carries the same Err — never a Value,
// never dropped — while the succeeding item's Result is untouched.
func TestSchedulerErrObservationCarriesThroughAsResultErr(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{
		{Item: "/", Metric: "disk.used_pct", Value: floatPtr(10)},
		{Item: "/broken", Metric: "disk.used_pct", Err: "permission denied"},
	}}
	reg := map[string]Plugin{"disk": plugin}
	ticks := &fakeTickers{}
	sched := NewScheduler(reg, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "disk", Plugin: "disk", Interval: time.Minute}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	batch := sink.all()[0]
	if len(batch) != 2 {
		t.Fatalf("batch = %+v, want 2 results (one good, one errored)", batch)
	}
	good := findResult(t, batch, "/", "disk.used_pct")
	if good.Value == nil || *good.Value != 10 || good.Err != "" {
		t.Errorf("good mount result = %+v, want a value of 10 and no error", good)
	}
	bad := findResult(t, batch, "/broken", "disk.used_pct")
	if bad.Err != "permission denied" || bad.Value != nil || bad.Label != nil {
		t.Errorf("broken mount result = %+v, want an error result carrying no value or label", bad)
	}

	cancel()
	<-done
}

func findResult(t *testing.T, batch []Result, item, metric string) Result {
	t.Helper()
	for _, r := range batch {
		if r.Item == item && r.Metric == metric {
			return r
		}
	}
	t.Fatalf("no result for item %q metric %q in %+v", item, metric, batch)
	return Result{}
}

func floatPtr(v float64) *float64 { return &v }
