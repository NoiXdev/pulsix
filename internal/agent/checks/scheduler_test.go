package checks

import (
	"context"
	"fmt"
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

	// A second check carries params, rebuilt as a FRESH map instance on
	// each Apply — exactly what a real re-push produces: every CheckConfig
	// arrives as its own JSON unmarshal, so an unchanged check comes back
	// as a different map instance with identical content, never the same
	// map value reused. reflect.DeepEqual (specsEqual's own comparison)
	// must treat that as unchanged; a naive == or pointer comparison would
	// not, and this case is what would have caught it.
	cfg := Config{Version: "v1", Checks: []Spec{
		{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second},
		{Key: "disk-params", Plugin: "cpu", Interval: time.Minute, Params: freshDiskParams()},
	}}
	sched.Apply(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	if len(tickers) != 2 {
		t.Fatalf("tickers after first apply = %d, want 2", len(tickers))
	}

	// Re-apply an equal-but-not-identical configuration: the plain check
	// is the literal same Spec value again, but the params-carrying
	// check's map is a brand new instance with the same content — no new
	// ticker for either, neither old one stopped.
	sched.Apply(Config{Version: "v1", Checks: []Spec{
		{Key: "cpu", Plugin: "cpu", Interval: 30 * time.Second},
		{Key: "disk-params", Plugin: "cpu", Interval: time.Minute, Params: freshDiskParams()},
	}})
	_, tickers2 := ticks.recorded()
	if len(tickers2) != 2 {
		t.Fatalf("tickers after re-applying an unchanged config = %d, want still 2", len(tickers2))
	}
	for i, tk := range tickers2 {
		if tk.wasStopped() {
			t.Errorf("ticker %d: an unchanged check's ticker must not be stopped by Apply, even when its params arrive as a new map instance", i)
		}
	}

	// The original tickers still drive their checks: proof the phase is
	// the SAME timers, not freshly-restarted ones.
	tickers2[0].c <- time.Now()
	tickers2[1].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })

	cancel()
	<-done
}

// freshDiskParams returns a new map instance with the same content every
// call — the shape a real CheckConfig's params_json produces after each
// independent json.Unmarshal, as opposed to a test reusing one map value
// across two Apply calls (which reflect.DeepEqual would trivially accept
// without proving anything about content-equality).
func freshDiskParams() map[string]any {
	return map[string]any{"mounts": []any{"/", "/data"}}
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

	// One tick first, both to prove the check is genuinely collecting and
	// because a delivered result is the only witness that Run's startup
	// loop has finished — without it the retirement below could take the
	// not-yet-running branch and prove nothing about a live check.
	oldTicker.c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	// The check disappears from the next configuration entirely.
	sched.Apply(Config{Version: "v2", Checks: nil})
	waitForSchedulerTest(t, time.Second, oldTicker.wasStopped)

	// Retired means its goroutine is gone, not merely that its ticker was
	// stopped: a tick pushed onto the old (now orphaned) channel must
	// collect nothing. A real ticker would never deliver after Stop, which
	// is exactly why the fake has to be the one asking the question.
	oldTicker.c <- time.Now()
	time.Sleep(20 * time.Millisecond)
	if n := len(sink.all()); n != 1 {
		t.Fatalf("results after the check was retired = %d, want 1: its goroutine is still running", n)
	}

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

// TestSchedulerApplyDuringShutdownNeverRacesTheWaitGroup pins the review's
// Important finding: a configuration can arrive (Apply -> startLocked ->
// spawnLocked -> wg.Add) at the exact instant Run's own context is
// cancelled — production's own shutdown can produce this timing directly
// (a manager message still in flight when the outer context ends) — and
// sync.WaitGroup forbids a positive Add racing a Wait once the counter
// can reach zero: a genuine panic, not a race-detector-only complaint.
//
// This drives the two goroutines against each other many times (no fake
// clock or ticker can stage a timing race — the whole point is the
// runtime's own scheduling), catching a misuse panic in the Apply
// goroutine's own recover rather than letting it crash the test binary
// unrecoverably. A single run proves little on its own (the window is a
// handful of nanoseconds); run repeatedly it either never panics (the
// fix holds) or fails loudly and immediately (it doesn't) — this is the
// same "many iterations, no sleep-based pacing" shape the reviewer asked
// for, applied to a mutex race rather than a channel one, since a
// WaitGroup misuse has no seam to force a deterministic interleaving the
// way a parked-goroutine channel does.
func TestSchedulerApplyDuringShutdownNeverRacesTheWaitGroup(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	reg := map[string]Plugin{"cpu": plugin}

	const iterations = 500
	for i := 0; i < iterations; i++ {
		sched := NewScheduler(reg, time.Now, (&fakeTickers{}).factory())
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- sched.Run(ctx, (&fakeSink{}).collect) }()

		var applyDone sync.WaitGroup
		applyDone.Add(1)
		panicked := make(chan any, 1)
		go func() {
			defer applyDone.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked <- r
				}
			}()
			// Racing Apply against Run's own shutdown: exactly the
			// production timing the review named (a CheckConfig still
			// arriving as the outer context is cancelled).
			sched.Apply(Config{Version: fmt.Sprintf("v%d", i), Checks: []Spec{
				{Key: fmt.Sprintf("cpu-%d", i), Plugin: "cpu", Interval: time.Millisecond},
			}})
		}()

		cancel()
		applyDone.Wait()
		if err := <-runDone; err != nil {
			t.Fatalf("iteration %d: Run = %v, want nil", i, err)
		}

		select {
		case r := <-panicked:
			t.Fatalf("iteration %d: Apply concurrent with shutdown panicked: %v "+
				"(sync.WaitGroup misuse: Add raced Wait)", i, r)
		default:
		}
	}
}

// TestSchedulerStopsEveryTickerItCreates: nothing the scheduler hands out
// may outlive it. Two ways a ticker used to be orphaned, both here: one
// created for a plugin the registry does not know (spawnLocked returns
// before starting a goroutine, so nothing ever owns it and Run's final
// map reset discarded it unstopped), and one created by an Apply arriving
// after Run has already returned — a configuration push still in flight
// at shutdown, which production really does produce.
//
// The post-shutdown Apply must also not claim its version: nothing is
// running it.
func TestSchedulerStopsEveryTickerItCreates(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{
		{Key: "cpu", Plugin: "cpu", Interval: time.Minute},
		{Key: "ghost", Plugin: "does-not-exist", Interval: time.Minute},
	}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, (&fakeSink{}).collect) }()
	waitForSchedulerTest(t, time.Second, func() bool { _, tk := ticks.recorded(); return len(tk) == 2 })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	sched.Apply(Config{Version: "v2", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})

	_, tickers := ticks.recorded()
	if len(tickers) != 2 {
		t.Errorf("tickers created = %d, want 2: an Apply after Run returned must create none, "+
			"since nothing is left to drive or stop one", len(tickers))
	}
	for i, tk := range tickers {
		if !tk.wasStopped() {
			t.Errorf("ticker %d was never stopped", i)
		}
	}
	// "" rather than "v1": the first configuration named a plugin this
	// build does not have, so it was never fully applied either (see
	// TestSchedulerPartialConfigNeverClaimsItsVersion). What this pins is
	// that the post-shutdown Apply did not move it to "v2".
	if got := sched.Version(); got != "" {
		t.Errorf("Version after an Apply that ran nothing = %q, want %q", got, "")
	}
}

// TestSchedulerStopsTheTickerOfARetiredUnknownPlugin: retiring a check
// whose plugin the registry does not know cannot go through its stop
// channel, because no goroutine is listening on it — the ticker has to be
// stopped directly, exactly as it is for a check retired before Run
// started.
//
// The ordering here is deliberate and not incidental: the ghost is added
// only once a tick has actually produced a result, which is the only
// witness that Run's startup loop has finished and `running` is true. Add
// it any earlier and the test could take stopLocked's OTHER branch and
// pass for the wrong reason.
func TestSchedulerStopsTheTickerOfARetiredUnknownPlugin(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	cpu := Spec{Key: "cpu", Plugin: "cpu", Interval: time.Minute}
	sched.Apply(Config{Version: "v1", Checks: []Spec{cpu}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	// Run is now demonstrably driving. A definition for a plugin this
	// build does not have arrives...
	sched.Apply(Config{Version: "v2", Checks: []Spec{cpu, {Key: "ghost", Plugin: "does-not-exist", Interval: time.Minute}}})
	_, tickers = ticks.recorded()
	if len(tickers) != 2 {
		t.Fatalf("tickers = %d, want 2 (the ticker is created even for an unknown plugin)", len(tickers))
	}
	// ...and is deleted again.
	sched.Apply(Config{Version: "v3", Checks: []Spec{cpu}})
	if !tickers[1].wasStopped() {
		t.Error("the retired unknown plugin's ticker was never stopped: no goroutine listens on its stop channel")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	_, all := ticks.recorded()
	for i, tk := range all {
		if !tk.wasStopped() {
			t.Errorf("ticker %d was never stopped", i)
		}
	}
}

// TestSchedulerPartialConfigNeverClaimsItsVersion: the manager stamps a
// version over the FULL set it intends. A check naming a plugin this
// build does not have is skipped — correctly, there is nothing to run it
// — but adopting the version anyway would make the heartbeat report
// convergence on a set the agent is not running, and processHeartbeat
// would then never push again. That is permanent divergence, not
// self-healing. A partial apply reports no version at all, so the manager
// keeps answering until the configuration is something this agent can
// actually run.
func TestSchedulerPartialConfigNeverClaimsItsVersion(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, (&fakeTickers{}).factory())

	full := Spec{Key: "cpu", Plugin: "cpu", Interval: time.Minute}
	if unrunnable := sched.Apply(Config{Version: "v1", Checks: []Spec{full}}); len(unrunnable) != 0 {
		t.Fatalf("unrunnable = %v, want none", unrunnable)
	}
	if got := sched.Version(); got != "v1" {
		t.Fatalf("Version after a fully runnable config = %q, want v1", got)
	}

	unrunnable := sched.Apply(Config{Version: "v2", Checks: []Spec{
		full, {Key: "ghost", Plugin: "does-not-exist", Interval: time.Minute},
	}})
	if len(unrunnable) != 1 || unrunnable[0] != "ghost" {
		t.Fatalf("unrunnable = %v, want [ghost] so the caller can say so in its log", unrunnable)
	}
	if got := sched.Version(); got != "" {
		t.Fatalf("Version after a partial config = %q, want \"\" — reporting v2 would tell the manager "+
			"the agent runs a set it does not, and nothing would ever push again", got)
	}

	// The runnable half is still installed, and a fully runnable config
	// afterwards converges normally.
	if unrunnable := sched.Apply(Config{Version: "v3", Checks: []Spec{full}}); len(unrunnable) != 0 {
		t.Fatalf("unrunnable = %v, want none", unrunnable)
	}
	if got := sched.Version(); got != "v3" {
		t.Fatalf("Version after the configuration was repaired = %q, want v3", got)
	}
}

// TestSchedulerApplyAfterRunReturnedAdoptsNothing: the same rule for the
// other way a configuration can fail to take effect — Run has already
// returned, so nothing is running anything at all.
func TestSchedulerApplyAfterRunReturnedAdoptsNothing(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, (&fakeTickers{}).factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, (&fakeSink{}).collect) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	sched.Apply(Config{Version: "v2", Checks: []Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})
	if got := sched.Version(); got != "v1" {
		t.Errorf("Version after an Apply that ran nothing = %q, want v1 unchanged", got)
	}
}
