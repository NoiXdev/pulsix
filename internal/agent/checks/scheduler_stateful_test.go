package checks

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

// lockedClock is a fake clock the test moves while scheduler goroutines
// read it.
type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *lockedClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// lockedNetCounters serves one eth0 sample the test sets between ticks and
// counts the reads, the only witness that a first (silent) collect ran.
type lockedNetCounters struct {
	mu    sync.Mutex
	stat  net.IOCountersStat
	calls int
}

func (c *lockedNetCounters) IOCounters(context.Context, bool) ([]net.IOCountersStat, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return []net.IOCountersStat{c.stat}, nil
}

func (c *lockedNetCounters) set(rxBytes, dropIn uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stat = net.IOCountersStat{Name: "eth0", BytesRecv: rxBytes, Dropin: dropIn}
}

func (c *lockedNetCounters) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func resultFor(t *testing.T, batches [][]Result, key, metric string) Result {
	t.Helper()
	for _, b := range batches {
		for _, r := range b {
			if r.CheckKey == key && r.Item == "eth0" && r.Metric == metric {
				return r
			}
		}
	}
	t.Fatalf("no result for key %q metric %q in %+v", key, metric, batches)
	return Result{}
}

// Two network keys watching the same interface tick in lockstep, 1 ms
// apart. Each must measure over its own interval, never over the 1 ms since
// the other key's collect: a single drop would otherwise read as 1000/s.
func TestSchedulerNetworkKeysOnOneInterfaceEachReportTheirOwnRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	clock := &lockedClock{t: t0}
	c := &lockedNetCounters{}
	c.set(0, 0)
	proto := newNetworkPlugin().(*networkPlugin)
	proto.Counters, proto.Interfaces, proto.Now = c, upIfaces("eth0"), clock.now

	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"network": proto}, clock.now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{
		{Key: "net-a", Plugin: "network", Interval: time.Minute},
		{Key: "net-b", Plugin: "network", Interval: time.Minute},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()
	_, tickers := ticks.recorded()
	a, b := tickers[0], tickers[1]

	// Baselines: a at t0, b at t0 + 1 ms.
	a.c <- t0
	waitForSchedulerTest(t, time.Second, func() bool { return c.callCount() == 1 })
	clock.set(t0.Add(time.Millisecond))
	b.c <- t0
	waitForSchedulerTest(t, time.Second, func() bool { return c.callCount() == 2 })

	// One interval later: 60 000 bytes arrived; one packet drops between
	// a's and b's collect.
	clock.set(t0.Add(time.Minute))
	c.set(60000, 0)
	a.c <- t0
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })
	clock.set(t0.Add(time.Minute + time.Millisecond))
	c.set(60000, 1)
	b.c <- t0
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })

	got := sink.all()
	checks := []struct {
		key, metric string
		want        float64
	}{
		{"net-a", "net.rx_bytes_per_s", 1000},
		{"net-a", "net.rx_drops_per_s", 0},
		{"net-b", "net.rx_bytes_per_s", 1000},
		{"net-b", "net.rx_drops_per_s", 1.0 / 60},
	}
	for _, tc := range checks {
		r := resultFor(t, got, tc.key, tc.metric)
		if r.Value == nil {
			t.Errorf("%s %s has no value: %+v", tc.key, tc.metric, r)
		} else if math.Abs(*r.Value-tc.want) > 1e-9 {
			t.Errorf("%s %s = %v, want %v", tc.key, tc.metric, *r.Value, tc.want)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// fakeStatefulPlugin is a prototype that hands every check key its own
// fakePlugin instance and records them in hand-out order.
type fakeStatefulPlugin struct {
	mu         sync.Mutex
	instances  []*fakePlugin
	protoCalls int
}

func (p *fakeStatefulPlugin) Name() string { return "stateful" }

func (p *fakeStatefulPlugin) Collect(context.Context, map[string]any) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protoCalls++
	return []Observation{{Metric: "m", Value: floatPtr(1)}}, nil
}

func (p *fakeStatefulPlugin) newInstance() Plugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	fp := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	p.instances = append(p.instances, fp)
	return fp
}

func (p *fakeStatefulPlugin) handedOut() ([]*fakePlugin, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*fakePlugin(nil), p.instances...), p.protoCalls
}

// A stateful plugin gets one instance per check key; an unchanged key keeps
// its instance across a re-apply, and a key whose definition changed gets a
// fresh one (fresh baselines), never the prototype itself.
func TestSchedulerStatefulPluginInstancePerKeyAndPerDefinition(t *testing.T) {
	proto := &fakeStatefulPlugin{}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"stateful": proto}, time.Now, ticks.factory())
	cfg := func(version string, bInterval time.Duration) Config {
		return Config{Version: version, Checks: []Spec{
			{Key: "a", Plugin: "stateful", Interval: time.Minute},
			{Key: "b", Plugin: "stateful", Interval: bInterval},
		}}
	}
	sched.Apply(cfg("v1", time.Minute))
	if inst, _ := proto.handedOut(); len(inst) != 2 {
		t.Fatalf("instances after the first apply = %d, want 2 (one per key)", len(inst))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()
	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	tickers[1].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })
	inst, protoCalls := proto.handedOut()
	if inst[0].callCount() != 1 || inst[1].callCount() != 1 || protoCalls != 0 {
		t.Fatalf("calls a=%d b=%d prototype=%d, want 1, 1, 0", inst[0].callCount(), inst[1].callCount(), protoCalls)
	}

	sched.Apply(cfg("v1", time.Minute)) // unchanged
	if inst, _ := proto.handedOut(); len(inst) != 2 {
		t.Fatalf("instances after an unchanged re-apply = %d, want still 2", len(inst))
	}

	sched.Apply(cfg("v2", 2*time.Minute)) // b's definition changed
	inst, _ = proto.handedOut()
	if len(inst) != 3 {
		t.Fatalf("instances after b changed = %d, want 3 (a fresh one for b)", len(inst))
	}
	_, tickers = ticks.recorded()
	tickers[2].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 3 })
	if inst[2].callCount() != 1 || inst[1].callCount() != 1 {
		t.Fatalf("calls old b=%d new b=%d, want 1, 1", inst[1].callCount(), inst[2].callCount())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}
