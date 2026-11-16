package probes

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

var (
	targetA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	targetB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

// fakeClock is a settable wall clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeTicker is a channel-driven ticker the test fires.
type fakeTicker struct {
	c       chan time.Time
	mu      sync.Mutex
	stopped bool
}

func (f *fakeTicker) C() <-chan time.Time { return f.c }

func (f *fakeTicker) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *fakeTicker) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// fakeTickers records every ticker the runner asks for.
type fakeTickers struct {
	mu        sync.Mutex
	intervals []time.Duration
	tickers   []*fakeTicker
}

func (f *fakeTickers) factory(d time.Duration) Ticker {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{c: make(chan time.Time)}
	f.intervals = append(f.intervals, d)
	f.tickers = append(f.tickers, t)
	return t
}

func (f *fakeTickers) all() []*fakeTicker {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeTicker(nil), f.tickers...)
}

// call is one probe the fake registry saw.
type call struct {
	plugin   string
	params   map[string]any
	address  string
	guard    *probe.Guard
	deadline time.Time
}

// blockingProbe is a probe.Func that records each call and, when gate is
// set, waits for a release (or its ctx) before answering obs.
type blockingProbe struct {
	mu       sync.Mutex
	calls    []call
	inFlight int
	maxSeen  int
	gate     chan struct{} // nil answers at once
	obs      []probe.Observation
	started  chan struct{}
}

func newBlockingProbe(gated bool, obs ...probe.Observation) *blockingProbe {
	b := &blockingProbe{obs: obs, started: make(chan struct{}, 64)}
	if gated {
		b.gate = make(chan struct{})
	}
	return b
}

func (b *blockingProbe) fn(plugin string) probe.Func {
	return func(ctx context.Context, g *probe.Guard, params map[string]any, address string, _ map[string]string) []probe.Observation {
		dl, _ := ctx.Deadline()
		b.mu.Lock()
		b.calls = append(b.calls, call{plugin: plugin, params: params, address: address, guard: g, deadline: dl})
		b.inFlight++
		b.maxSeen = max(b.maxSeen, b.inFlight)
		b.mu.Unlock()
		b.started <- struct{}{}
		defer func() {
			b.mu.Lock()
			b.inFlight--
			b.mu.Unlock()
		}()
		if b.gate != nil {
			select {
			case <-b.gate:
			case <-ctx.Done():
				return nil
			}
		}
		return b.obs
	}
}

func (b *blockingProbe) release() { b.gate <- struct{}{} }

func (b *blockingProbe) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *blockingProbe) max() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxSeen
}

func (b *blockingProbe) waitStarted(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-b.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("a probe did not start")
		}
	}
}

// sink collects the batches the runner hands out.
type sink struct {
	mu      sync.Mutex
	batches [][]Result
	got     chan struct{}
}

func newSink() *sink { return &sink{got: make(chan struct{}, 64)} }

func (s *sink) add(r []Result) {
	s.mu.Lock()
	s.batches = append(s.batches, r)
	s.mu.Unlock()
	s.got <- struct{}{}
}

func (s *sink) all() [][]Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]Result(nil), s.batches...)
}

func (s *sink) wait(t *testing.T) []Result {
	t.Helper()
	select {
	case <-s.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no results reached the sink")
	}
	b := s.all()
	return b[len(b)-1]
}

func spec(target uuid.UUID, key, plugin string, intervalS int32) probeconfig.Spec {
	return probeconfig.Spec{
		TargetHostID: target, CheckKey: key, Plugin: plugin,
		ParamsJSON: `{"port":443}`, IntervalSeconds: intervalS, Address: "192.0.2.10",
	}
}

func up(plugin string) probe.Observation { return probe.Value(plugin+".up", 1) }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
