package checks

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

// localTestRun starts sched.Run and returns its sink, a cancel func and a
// channel that carries Run's return value.
func localTestRun(sched *Scheduler) (*fakeSink, context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()
	return sink, cancel, done
}

// stopRun cancels Run, waits for it and checks every ticker the scheduler
// ever created was stopped.
func stopRun(t *testing.T, cancel context.CancelFunc, done <-chan error, ticks *fakeTickers) {
	t.Helper()
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

func resultKeys(batches [][]Result) []string {
	var keys []string
	for _, b := range batches {
		for _, r := range b {
			keys = append(keys, r.CheckKey)
		}
	}
	sort.Strings(keys)
	return keys
}

func TestSchedulerRunsLocalChecksNextToTheManagers(t *testing.T) {
	cpu := &fakePlugin{obs: []Observation{{Metric: "cpu.used_pct", Value: floatPtr(1)}}}
	mem := &fakePlugin{obs: []Observation{{Metric: "mem.used_pct", Value: floatPtr(2)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": cpu, "mem": mem}, time.Now, ticks.factory())

	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "a", Plugin: "cpu", Interval: time.Minute}}})
	if shadowed := sched.ApplyLocal([]Spec{{Key: "local-x", Plugin: "mem", Interval: time.Minute}}); len(shadowed) != 0 {
		t.Fatalf("shadowed = %v, want none", shadowed)
	}

	sink, cancel, done := localTestRun(sched)
	_, tickers := ticks.recorded()
	if len(tickers) != 2 {
		t.Fatalf("tickers = %d, want 2 (one manager, one local check)", len(tickers))
	}
	tickers[0].c <- time.Now()
	tickers[1].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })

	if got, want := resultKeys(sink.all()), []string{"a", "local-x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("result keys = %v, want %v", got, want)
	}
	if cpu.callCount() != 1 || mem.callCount() != 1 {
		t.Errorf("calls cpu=%d mem=%d, want 1 each", cpu.callCount(), mem.callCount())
	}
	stopRun(t, cancel, done, ticks)
}

func TestSchedulerApplyLocalUnchangedKeepsPhase(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	local := func() []Spec {
		return []Spec{{Key: "local-x", Plugin: "cpu", Interval: time.Minute, Params: freshDiskParams()}}
	}
	sched.ApplyLocal(local())

	sink, cancel, done := localTestRun(sched)
	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	// The same local set again (fresh params map, same content), and a
	// manager push around it: neither touches the local check's timer.
	sched.ApplyLocal(local())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "a", Plugin: "cpu", Interval: time.Minute}}})
	sched.ApplyLocal(local())

	_, tickers = ticks.recorded()
	if len(tickers) != 2 {
		t.Fatalf("tickers = %d, want 2 (the local check's own, then the manager's a)", len(tickers))
	}
	if tickers[0].wasStopped() {
		t.Error("an unchanged local check's ticker was stopped")
	}
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })
	stopRun(t, cancel, done, ticks)
}

func TestSchedulerManagerKeyShadowsALocalCheckAndReleasesIt(t *testing.T) {
	cpu := &fakePlugin{obs: []Observation{{Metric: "cpu.used_pct", Value: floatPtr(1)}}}
	mem := &fakePlugin{obs: []Observation{{Metric: "mem.used_pct", Value: floatPtr(2)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": cpu, "mem": mem}, time.Now, ticks.factory())
	localA := Spec{Key: "a", Plugin: "mem", Interval: time.Minute}
	if shadowed := sched.ApplyLocal([]Spec{localA}); len(shadowed) != 0 {
		t.Fatalf("shadowed = %v, want none (no manager set yet)", shadowed)
	}

	sink, cancel, done := localTestRun(sched)
	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	// The manager's a appears: it runs, the local a stops.
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "a", Plugin: "cpu", Interval: time.Minute}}})
	if got := sched.Shadowed(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Shadowed = %v, want [a]", got)
	}
	_, tickers = ticks.recorded()
	if len(tickers) != 2 {
		t.Fatalf("tickers = %d, want 2", len(tickers))
	}
	waitForSchedulerTest(t, time.Second, tickers[0].wasStopped)
	tickers[1].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })
	if cpu.callCount() != 1 || mem.callCount() != 1 {
		t.Errorf("calls cpu=%d mem=%d, want 1 each: the manager's a must collect with its own plugin", cpu.callCount(), mem.callCount())
	}

	// The manager's a goes: the local a starts again.
	sched.Apply(Config{Version: "v2"})
	if got := sched.Shadowed(); len(got) != 0 {
		t.Errorf("Shadowed = %v, want none", got)
	}
	_, tickers = ticks.recorded()
	if len(tickers) != 3 {
		t.Fatalf("tickers = %d, want 3", len(tickers))
	}
	waitForSchedulerTest(t, time.Second, tickers[1].wasStopped)
	tickers[2].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 3 })
	if mem.callCount() != 2 {
		t.Errorf("mem calls = %d, want 2: the released local a collects again", mem.callCount())
	}
	stopRun(t, cancel, done, ticks)
}

// TestSchedulerManagerKeyShadowsALocalCheckOfTheSameSpec: the local and the
// manager's definition are identical, and the local one still yields — the
// running check belongs to whichever set owns the key, not to whichever
// spec happens to match.
func TestSchedulerManagerKeyShadowsALocalCheckOfTheSameSpec(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	a := Spec{Key: "a", Plugin: "cpu", Interval: time.Minute}
	sched.ApplyLocal([]Spec{a})
	sched.Apply(Config{Version: "v1", Checks: []Spec{a}})

	_, tickers := ticks.recorded()
	if len(tickers) != 2 || !tickers[0].wasStopped() {
		t.Fatalf("tickers = %d (first stopped=%v), want the local a stopped and the manager's started",
			len(tickers), len(tickers) > 0 && tickers[0].wasStopped())
	}
	if got := sched.Shadowed(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Shadowed = %v, want [a]", got)
	}
}

func TestSchedulerApplyLocalReportsKeysTheManagerAlreadyRuns(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{
		{Key: "b", Plugin: "cpu", Interval: time.Minute},
		{Key: "a", Plugin: "cpu", Interval: time.Minute},
	}})

	sink, cancel, done := localTestRun(sched)
	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	shadowed := sched.ApplyLocal([]Spec{
		{Key: "b", Plugin: "cpu", Interval: time.Second},
		{Key: "local-y", Plugin: "cpu", Interval: time.Minute},
		{Key: "a", Plugin: "cpu", Interval: time.Second},
	})
	if want := []string{"a", "b"}; !reflect.DeepEqual(shadowed, want) {
		t.Errorf("shadowed = %v, want %v (sorted)", shadowed, want)
	}
	if got := sched.Shadowed(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Shadowed = %v, want [a b]", got)
	}
	_, tickers = ticks.recorded()
	if len(tickers) != 3 {
		t.Fatalf("tickers = %d, want 3 (only local-y is new)", len(tickers))
	}
	if tickers[0].wasStopped() || tickers[1].wasStopped() {
		t.Error("a shadowed local check touched the manager's running check")
	}
	stopRun(t, cancel, done, ticks)
}

func TestSchedulerLocalSetLeavesVersionAndUnrunnableToTheManager(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, (&fakeTickers{}).factory())
	a := Spec{Key: "a", Plugin: "cpu", Interval: time.Minute}

	if unrunnable := sched.Apply(Config{Version: "v1", Checks: []Spec{a}}); len(unrunnable) != 0 {
		t.Fatalf("unrunnable = %v, want none", unrunnable)
	}
	// A local set with a plugin this build does not have, and one that
	// shadows a: neither is the manager's business.
	sched.ApplyLocal([]Spec{
		{Key: "local-ghost", Plugin: "does-not-exist", Interval: time.Minute},
		{Key: "a", Plugin: "does-not-exist", Interval: time.Minute},
	})
	if got := sched.Version(); got != "v1" {
		t.Errorf("Version after ApplyLocal = %q, want v1 unchanged", got)
	}

	if unrunnable := sched.Apply(Config{Version: "v2", Checks: []Spec{a}}); len(unrunnable) != 0 {
		t.Errorf("unrunnable = %v, want none: local checks are not the manager's set", unrunnable)
	}
	if got := sched.Version(); got != "v2" {
		t.Errorf("Version = %q, want v2", got)
	}

	unrunnable := sched.Apply(Config{Version: "v3", Checks: []Spec{a, {Key: "ghost", Plugin: "does-not-exist", Interval: time.Minute}}})
	if !reflect.DeepEqual(unrunnable, []string{"ghost"}) {
		t.Errorf("unrunnable = %v, want [ghost]", unrunnable)
	}
	if got := sched.Version(); got != "" {
		t.Errorf("Version after a partial manager config = %q, want \"\"", got)
	}

	// The manager drops a: the local a (unknown plugin) is released and
	// still not counted against the manager's set.
	if unrunnable := sched.Apply(Config{Version: "v4"}); len(unrunnable) != 0 {
		t.Errorf("unrunnable = %v, want none", unrunnable)
	}
	if got := sched.Version(); got != "v4" {
		t.Errorf("Version = %q, want v4", got)
	}
}

func TestSchedulerEmptyLocalSetStopsOnlyLocalChecks(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "a", Plugin: "cpu", Interval: time.Minute}}})
	sched.ApplyLocal([]Spec{
		{Key: "local-1", Plugin: "cpu", Interval: time.Minute},
		{Key: "local-2", Plugin: "cpu", Interval: time.Minute},
		{Key: "a", Plugin: "cpu", Interval: time.Second},
	})

	sink, cancel, done := localTestRun(sched)
	_, tickers := ticks.recorded()
	if len(tickers) != 3 {
		t.Fatalf("tickers = %d, want 3", len(tickers))
	}
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	if shadowed := sched.ApplyLocal(nil); len(shadowed) != 0 {
		t.Errorf("shadowed = %v, want none", shadowed)
	}
	waitForSchedulerTest(t, time.Second, func() bool { return tickers[1].wasStopped() && tickers[2].wasStopped() })
	if tickers[0].wasStopped() {
		t.Error("an empty local set stopped the manager's check")
	}
	if got := sched.Shadowed(); len(got) != 0 {
		t.Errorf("Shadowed = %v, want none", got)
	}
	if got := sched.Version(); got != "v1" {
		t.Errorf("Version = %q, want v1", got)
	}
	stopRun(t, cancel, done, ticks)
}

func TestSchedulerApplyLocalBeforeRunAndAfterRunReturned(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": plugin}, time.Now, ticks.factory())
	sched.ApplyLocal([]Spec{{Key: "local-x", Plugin: "cpu", Interval: time.Minute}})
	if plugin.callCount() != 0 {
		t.Fatal("a local check collected before Run")
	}

	sink, cancel, done := localTestRun(sched)
	_, tickers := ticks.recorded()
	if len(tickers) != 1 {
		t.Fatalf("tickers = %d, want 1", len(tickers))
	}
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })
	if got := sink.all()[0][0].CheckKey; got != "local-x" {
		t.Errorf("CheckKey = %q, want local-x", got)
	}
	stopRun(t, cancel, done, ticks)

	sched.ApplyLocal([]Spec{{Key: "local-y", Plugin: "cpu", Interval: time.Minute}})
	_, tickers = ticks.recorded()
	if len(tickers) != 1 {
		t.Errorf("tickers = %d, want 1: an ApplyLocal after Run returned must create none", len(tickers))
	}
}
