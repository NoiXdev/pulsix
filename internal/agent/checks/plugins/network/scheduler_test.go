package network

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/net"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// The network plugin under the real scheduler: the per-key cloning lives in
// internal/agent/checks (pinned there with a fake StatefulPlugin); this file
// pins that network's own state survives it. The ticker, sink and wait
// helpers below are this package's minimal copies of the scheduler tests'
// fakes (backlog 3.52).

// fakeTicker is a channel-driven ticker the test controls; the channel is
// buffered so a push never waits for the check's goroutine.
type fakeTicker struct{ c chan time.Time }

func (f *fakeTicker) C() <-chan time.Time { return f.c }

func (f *fakeTicker) Stop() {}

// fakeTickers hands out fakeTickers and records them in hand-out order.
type fakeTickers struct {
	mu      sync.Mutex
	tickers []*fakeTicker
}

func (f *fakeTickers) factory() func(time.Duration) checks.Ticker {
	return func(time.Duration) checks.Ticker {
		f.mu.Lock()
		defer f.mu.Unlock()
		ft := &fakeTicker{c: make(chan time.Time, 4)}
		f.tickers = append(f.tickers, ft)
		return ft
	}
}

func (f *fakeTickers) recorded() []*fakeTicker {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeTicker(nil), f.tickers...)
}

// fakeSink records every batch handed to it, safe for concurrent scheduler
// goroutines to call.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]checks.Result
}

func (s *fakeSink) collect(r []checks.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, r)
}

func (s *fakeSink) all() [][]checks.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]checks.Result(nil), s.batches...)
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

func resultFor(t *testing.T, batches [][]checks.Result, key, metric string) checks.Result {
	t.Helper()
	for _, b := range batches {
		for _, r := range b {
			if r.CheckKey == key && r.Item == "eth0" && r.Metric == metric {
				return r
			}
		}
	}
	t.Fatalf("no result for key %q metric %q in %+v", key, metric, batches)
	return checks.Result{}
}

// Two network keys watching the same interface tick in lockstep, 1 ms
// apart. Each must measure over its own interval, never over the 1 ms since
// the other key's collect: a single drop would otherwise read as 1000/s.
func TestSchedulerNetworkKeysOnOneInterfaceEachReportTheirOwnRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	clock := &lockedClock{t: t0}
	c := &lockedNetCounters{}
	c.set(0, 0)
	proto := New().(*networkPlugin)
	proto.Counters, proto.Interfaces, proto.Now = c, upIfaces("eth0"), clock.now

	ticks := &fakeTickers{}
	sched := checks.NewScheduler(map[string]checks.Plugin{"network": proto}, clock.now, ticks.factory())
	sched.Apply(checks.Config{Version: "v1", Checks: []checks.Spec{
		{Key: "net-a", Plugin: "network", Interval: time.Minute},
		{Key: "net-b", Plugin: "network", Interval: time.Minute},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()
	tickers := ticks.recorded()
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
	cases := []struct {
		key, metric string
		want        float64
	}{
		{"net-a", "net.rx_bytes_per_s", 1000},
		{"net-a", "net.rx_drops_per_s", 0},
		{"net-b", "net.rx_bytes_per_s", 1000},
		{"net-b", "net.rx_drops_per_s", 1.0 / 60},
	}
	for _, tc := range cases {
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
