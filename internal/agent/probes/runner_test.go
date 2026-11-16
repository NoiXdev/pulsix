package probes

import (
	"context"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// newTestRunner wires a runner against reg, a fake clock and fake tickers.
func newTestRunner(reg probe.Registry) (*Runner, *fakeClock, *fakeTickers) {
	clock := newFakeClock()
	tickers := &fakeTickers{}
	r := NewRunner(Deps{Registry: reg, Now: clock.Now, Ticker: tickers.factory, Log: slog.New(slog.DiscardHandler)})
	return r, clock, tickers
}

func TestVersionIsEmptyUntilAConfigArrives(t *testing.T) {
	t.Parallel()
	r, _, _ := newTestRunner(probe.Registry{})
	if v := r.Version(); v != "" {
		t.Fatalf("Version before any config = %q, want empty", v)
	}
	specs := []probeconfig.Spec{spec(targetA, "web", "tcp", 30)}
	r.Apply(specs)
	if v := r.Version(); v != probeconfig.Version(specs) {
		t.Fatalf("Version = %q, want %q", v, probeconfig.Version(specs))
	}
	r.Apply(nil)
	if v := r.Version(); v != probeconfig.Version(nil) {
		t.Fatalf("Version of the empty set = %q, want the empty encoding's %q", v, probeconfig.Version(nil))
	}
}

// TestFirstRunIsWithinAMinuteForLongIntervals: the first-run window is
// min(interval, firstRunWindow), so a slot reassigned to a new agent
// reports within a minute even with an hourly or daily interval, still
// spread by the slot inside that window.
func TestFirstRunIsWithinAMinuteForLongIntervals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		interval, window time.Duration
	}{
		{10 * time.Second, 10 * time.Second},
		{30 * time.Second, 30 * time.Second},
		{time.Minute, time.Minute},
		{time.Hour, time.Minute},
		{24 * time.Hour, time.Minute},
	} {
		seen := map[time.Duration]bool{}
		for _, slot := range []struct {
			target uuid.UUID
			key    string
		}{{targetA, "web"}, {targetB, "web"}, {targetA, "dns"}, {targetB, "dns"}} {
			off := firstOffset(slot.target, slot.key, tc.interval)
			if off < 0 || off >= tc.window {
				t.Fatalf("interval %s: offset %s outside [0, %s)", tc.interval, off, tc.window)
			}
			seen[off] = true
		}
		if len(seen) < 2 {
			t.Fatalf("interval %s: offsets do not depend on the slot: %v", tc.interval, seen)
		}
	}
}

func TestFirstRunIsSpreadOverTheIntervalByTheSlot(t *testing.T) {
	t.Parallel()
	interval := 60 * time.Second
	offA := firstOffset(targetA, "web", interval)
	offB := firstOffset(targetB, "web", interval)
	offA2 := firstOffset(targetA, "dns", interval)
	for _, off := range []time.Duration{offA, offB, offA2} {
		if off < 0 || off >= interval {
			t.Fatalf("offset %s outside [0, %s)", off, interval)
		}
	}
	if offA == offB || offA == offA2 {
		t.Fatalf("offsets do not depend on the slot: %s %s %s", offA, offB, offA2)
	}
	if offA != firstOffset(targetA, "web", interval) {
		t.Fatal("the offset of a slot is not stable")
	}

	// The runner waits the offset out before the first run.
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	start := clock.Now()
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 60)})
	s := newSink()
	ctx := t.Context()
	clock.set(start.Add(offA - time.Nanosecond))
	r.dispatch(ctx, s.add)
	r.wg.Wait()
	if n := b.callCount(); n != 0 {
		t.Fatalf("probed %d times before the slot's offset", n)
	}
	clock.set(start.Add(offA))
	r.dispatch(ctx, s.add)
	r.wg.Wait()
	if n := b.callCount(); n != 1 {
		t.Fatalf("probed %d times at the slot's offset, want 1", n)
	}
	// Then every interval, not before.
	clock.set(start.Add(offA + interval - time.Nanosecond))
	r.dispatch(ctx, s.add)
	r.wg.Wait()
	if n := b.callCount(); n != 1 {
		t.Fatalf("probed again before the interval passed (%d calls)", n)
	}
	clock.set(start.Add(offA + interval))
	r.dispatch(ctx, s.add)
	r.wg.Wait()
	if n := b.callCount(); n != 2 {
		t.Fatalf("probed %d times after one interval, want 2", n)
	}
}

func TestUnchangedSlotKeepsItsScheduleAcrossApply(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	interval := 60 * time.Second
	start := clock.Now()
	keep := spec(targetA, "web", "tcp", 60)
	r.Apply([]probeconfig.Spec{keep})
	ctx := t.Context()
	s := newSink()
	first := start.Add(firstOffset(targetA, "web", interval))
	clock.set(first)
	r.dispatch(ctx, s.add)
	r.wg.Wait()

	// A new config naming the same slot unchanged (plus another) leaves its
	// phase alone: no extra run before first+interval.
	clock.set(first.Add(time.Second))
	r.Apply([]probeconfig.Spec{keep, spec(targetB, "web", "tcp", 60)})
	clock.set(first.Add(interval - time.Nanosecond))
	r.dispatch(ctx, s.add)
	r.wg.Wait()
	if got := slotNext(r, targetA, "web"); !got.Equal(first.Add(interval)) {
		t.Fatalf("kept slot's next run = %s, want %s (its schedule kept)", got, first.Add(interval))
	}

	// A changed slot (params) is a new slot with a fresh first run.
	changed := keep
	changed.ParamsJSON = `{"port":8443}`
	now := first.Add(interval - time.Nanosecond)
	r.Apply([]probeconfig.Spec{changed})
	if got, want := slotNext(r, targetA, "web"), now.Add(firstOffset(targetA, "web", interval)); !got.Equal(want) {
		t.Fatalf("changed slot's next run = %s, want %s (rescheduled from the apply)", got, want)
	}
}

func slotNext(r *Runner, target interface{ String() string }, key string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, s := range r.slots {
		if k.target.String() == target.String() && k.check == key {
			return s.next
		}
	}
	return time.Time{}
}

func TestRemovedSlotStopsAndItsInFlightResultIsDiscarded(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 60)})
	clock.advance(time.Minute)
	ctx := t.Context()
	s := newSink()
	r.dispatch(ctx, s.add)
	b.waitStarted(t, 1)

	r.Apply(nil)
	b.release()
	r.wg.Wait()
	if got := s.all(); len(got) != 0 {
		t.Fatalf("a removed slot's in-flight result reached the sink: %+v", got)
	}
	clock.advance(time.Hour)
	r.dispatch(ctx, s.add)
	r.wg.Wait()
	if n := b.callCount(); n != 1 {
		t.Fatalf("a removed slot was probed again (%d calls)", n)
	}
}

func TestReplacedSlotDiscardsTheOldRoundsResult(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	old := spec(targetA, "web", "tcp", 60)
	r.Apply([]probeconfig.Spec{old})
	clock.advance(time.Minute)
	ctx := t.Context()
	s := newSink()
	r.dispatch(ctx, s.add)
	b.waitStarted(t, 1)

	changed := old
	changed.Address = "192.0.2.99"
	r.Apply([]probeconfig.Spec{changed})
	b.release()
	r.wg.Wait()
	if got := s.all(); len(got) != 0 {
		t.Fatalf("the old round of a changed slot reached the sink: %+v", got)
	}
}

func TestKeptSlotDeliversItsInFlightResult(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	keep := spec(targetA, "web", "tcp", 60)
	r.Apply([]probeconfig.Spec{keep})
	clock.advance(time.Minute)
	ctx := t.Context()
	s := newSink()
	r.dispatch(ctx, s.add)
	b.waitStarted(t, 1)
	r.Apply([]probeconfig.Spec{keep, spec(targetB, "web", "tcp", 60)})
	b.release()
	if got := s.wait(t); len(got) != 1 || got[0].TargetHostID != targetA {
		t.Fatalf("results = %+v, want the kept slot's one", got)
	}
	r.wg.Wait()
}

func TestAtMostFourProbesInFlight(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, tickers := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	var specs []probeconfig.Spec
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		specs = append(specs, spec(targetA, key, "tcp", 30))
	}
	r.Apply(specs)
	clock.advance(time.Minute) // every slot due
	s := newSink()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx, s.add); close(done) }()

	b.waitStarted(t, MaxConcurrent)
	if n := b.callCount(); n != MaxConcurrent {
		t.Fatalf("%d probes started at once, want %d", n, MaxConcurrent)
	}
	// Each finished probe frees a place another due slot takes at once,
	// without waiting for a tick.
	for range 6 {
		b.release()
		b.waitStarted(t, 1)
	}
	for range MaxConcurrent {
		b.release()
	}
	waitFor(t, func() bool { return len(s.all()) == 10 })
	if m := b.max(); m != MaxConcurrent {
		t.Fatalf("at most %d probes ran at once, want %d", m, MaxConcurrent)
	}
	if n := b.callCount(); n != 10 {
		t.Fatalf("calls = %d, want 10 (each slot once)", n)
	}
	cancel()
	<-done
	if len(tickers.all()) == 0 {
		t.Fatal("no ticker while slots were assigned")
	}
}

func TestProbeDeadlineIsTheIntervalCappedAtAMinute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		interval int32
		want     time.Duration
	}{{30, 30 * time.Second}, {60, time.Minute}, {300, time.Minute}} {
		b := newBlockingProbe(false, up("tcp"))
		r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
		r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", tc.interval)})
		clock.advance(time.Hour)
		before := time.Now()
		r.dispatch(t.Context(), func([]Result) {})
		r.wg.Wait()
		dl := b.calls[0].deadline
		if dl.IsZero() {
			t.Fatalf("interval %d s: the probe ran without a deadline", tc.interval)
		}
		if got := dl.Sub(before); got > tc.want+time.Second || got < tc.want-5*time.Second {
			t.Fatalf("interval %d s: deadline in %s, want about %s", tc.interval, got, tc.want)
		}
	}
}

func TestResultsCarryTheTargetAndOneAtPerRound(t *testing.T) {
	t.Parallel()
	label := "ok"
	clockRef := newFakeClock()
	reg := probe.Registry{"http": func(context.Context, *probe.Guard, map[string]any, string, map[string]string) []probe.Observation {
		clockRef.advance(3 * time.Second) // the probe takes time
		return []probe.Observation{
			probe.Value("http.up", 1),
			{Metric: "http.json.value", Item: "status", Label: &label},
			{Metric: "http.status_code", Err: "boom"},
		}
	}}
	tickers := &fakeTickers{}
	r := NewRunner(Deps{Registry: reg, Now: clockRef.Now, Ticker: tickers.factory})
	r.Apply([]probeconfig.Spec{spec(targetB, "site", "http", 60)})
	clockRef.advance(time.Minute)
	roundStart := clockRef.Now()
	s := newSink()
	r.dispatch(t.Context(), s.add)
	got := s.wait(t)
	r.wg.Wait()
	if len(got) != 3 {
		t.Fatalf("results = %+v, want 3", got)
	}
	for _, res := range got {
		if res.TargetHostID != targetB || res.CheckKey != "site" {
			t.Errorf("result %+v does not name the slot", res)
		}
		if !res.At.Equal(roundStart) {
			t.Errorf("result %s at %s, want the round's start %s", res.Metric, res.At, roundStart)
		}
	}
	if got[0].Value == nil || *got[0].Value != 1 {
		t.Errorf("value not carried: %+v", got[0])
	}
	if got[1].Item != "status" || got[1].Label == nil || *got[1].Label != "ok" {
		t.Errorf("item/label not carried: %+v", got[1])
	}
	if got[2].Err != "boom" {
		t.Errorf("error not carried: %+v", got[2])
	}
}

func TestPanickingProbeIsContainedAndRunsAgain(t *testing.T) {
	t.Parallel()
	calls := 0
	reg := probe.Registry{"tcp": func(context.Context, *probe.Guard, map[string]any, string, map[string]string) []probe.Observation {
		calls++
		panic("hostile input")
	}}
	r, clock, _ := newTestRunner(reg)
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 30)})
	s := newSink()
	for range 2 {
		clock.advance(time.Minute)
		r.dispatch(t.Context(), s.add)
		r.wg.Wait()
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (the slot keeps its schedule after a panic)", calls)
	}
	if len(s.all()) != 0 {
		t.Fatalf("a panicked round produced results: %+v", s.all())
	}
}

func TestRunTicksOnlyWhileSlotsAreAssignedAndStopsCleanly(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, tickers := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx, newSink().add); close(done) }()

	time.Sleep(20 * time.Millisecond)
	if n := len(tickers.all()); n != 0 {
		t.Fatalf("%d tickers with no slots assigned, want 0", n)
	}
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 30)})
	waitFor(t, func() bool { return len(tickers.all()) == 1 })
	if d := tickers.intervals[0]; d != tick {
		t.Fatalf("ticker interval = %s, want %s", d, tick)
	}
	clock.advance(time.Minute)
	tickers.all()[0].c <- clock.Now()
	b.waitStarted(t, 1)

	r.Apply([]probeconfig.Spec{})
	waitFor(t, func() bool { return tickers.all()[0].isStopped() })

	// Shutdown cancels the in-flight probe and waits for it.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	b.mu.Lock()
	inFlight := b.inFlight
	b.mu.Unlock()
	if inFlight != 0 {
		t.Fatalf("Run returned with %d probes still running", inFlight)
	}
}

// TestReplacingConfigsLeaksNoGoroutines applies a stream of changing sets
// while probes run, then shuts down: the goroutine count returns to where
// it started.
func TestReplacingConfigsLeaksNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, tickers := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx, func([]Result) {}); close(done) }()
	for i := range 50 {
		var specs []probeconfig.Spec
		for j := range i % 7 {
			specs = append(specs, spec(targetA, string(rune('a'+j)), "tcp", int32(10+i)))
		}
		r.Apply(specs)
		clock.advance(time.Minute)
		if ts := tickers.all(); len(ts) > 0 && !ts[len(ts)-1].isStopped() {
			select {
			case ts[len(ts)-1].c <- clock.Now():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	cancel()
	<-done
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before })
	if b.callCount() == 0 {
		t.Fatal("no probe ran; the test proves nothing")
	}
}

func TestDuplicateSlotsRunOnceAndKeepTheManagersVersion(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	specs := []probeconfig.Spec{spec(targetA, "web", "tcp", 30), spec(targetA, "web", "tcp", 30)}
	r.Apply(specs)
	if r.Version() != probeconfig.Version(specs) {
		t.Fatal("the reported version is not the one of the set the manager sent")
	}
	clock.advance(time.Minute)
	r.dispatch(t.Context(), func([]Result) {})
	r.wg.Wait()
	if n := b.callCount(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestIntervalBelowTheMinimumIsRaised(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 0)})
	ctx := t.Context()
	for range 20 {
		clock.advance(time.Second)
		r.dispatch(ctx, func([]Result) {})
		r.wg.Wait()
	}
	if n := b.callCount(); n > 2 {
		t.Fatalf("a zero interval probed %d times in 20 s, want at most 2 (the 10 s minimum)", n)
	}
}
