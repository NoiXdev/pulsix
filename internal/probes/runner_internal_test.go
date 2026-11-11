package probes

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func synthetic(key, plugin string, interval time.Duration) checks.Resolved {
	return checks.Resolved{Key: key, Plugin: plugin, Params: map[string]any{"key": key}, Interval: interval}
}

// harness is one runner over fakes, driven tick by tick.
type harness struct {
	r      *Runner
	clock  *fakeClock
	locker *fakeLocker
	world  *fakeWorld
	rec    *fakeRecorder
	probe  *fakeProbe
	org    uuid.UUID
	host   uuid.UUID
}

func newHarness(t *testing.T, edit func(*harness, *Deps)) *harness {
	t.Helper()
	h := &harness{
		clock:  &fakeClock{now: t0},
		locker: &fakeLocker{allow: true},
		world:  newFakeWorld(),
		rec:    &fakeRecorder{},
		probe:  &fakeProbe{},
		org:    uuid.Must(uuid.NewV7()),
		host:   uuid.Must(uuid.NewV7()),
	}
	d := Deps{
		Orgs: fakeOrgs{ids: []uuid.UUID{h.org}}, Resolver: h.world, Hosts: h.world,
		Recorder: h.rec, Locker: h.locker, Probe: h.probe.Run, Clock: h.clock.Now,
	}
	if edit != nil {
		edit(h, &d)
	}
	h.r = NewRunner(d)
	return h
}

// tickAt runs one tick at t0+at and waits for every probe it started.
func (h *harness) tickAt(at time.Duration) {
	h.clock.Set(t0.Add(at))
	h.r.tick(context.Background())
	h.r.wg.Wait()
}

// running is how many probes the runner has in flight: launches happen
// synchronously inside tick, so this is exact once tick returns.
func (h *harness) running() int {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	return len(h.r.inFlight)
}

func TestNoLockNoProbe(t *testing.T) {
	h := newHarness(t, nil)
	h.locker.allow = false
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	h.tickAt(0)
	h.tickAt(time.Minute)
	if keys := h.probe.Keys(); len(keys) != 0 {
		t.Fatalf("probed %v without holding the lock", keys)
	}
}

func TestProbesEachCheckOncePerInterval(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("fast", "tcp", 60*time.Second), synthetic("slow", "http", 300*time.Second))

	h.tickAt(0)
	if got := sorted(h.probe.Keys()); !slices.Equal(got, []string{"fast", "slow"}) {
		t.Fatalf("t=0 probed %v, want both checks", got)
	}
	for at := 5 * time.Second; at < 60*time.Second; at += 5 * time.Second {
		h.tickAt(at)
	}
	if n := len(h.probe.Keys()); n != 2 {
		t.Fatalf("probed %d times before either interval elapsed, want 2", n)
	}
	h.tickAt(60 * time.Second)
	if got := h.probe.Keys(); len(got) != 3 || got[2] != "fast" {
		t.Fatalf("t=60 probed %v, want the 60-s check alone again", got)
	}
	h.tickAt(300 * time.Second)
	if got := sorted(h.probe.Keys()[3:]); !slices.Equal(got, []string{"fast", "slow"}) {
		t.Fatalf("t=300 probed %v, want both checks", got)
	}
	for _, c := range h.probe.Calls() {
		if c.address != "192.0.2.1" {
			t.Fatalf("probed address %q, want the host's", c.address)
		}
	}
}

func TestRecordsWithHostKeyAndClock(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	h.tickAt(0)
	b := h.rec.Batches()
	if len(b) != 1 || b[0].org != h.org || len(b[0].obs) != 1 {
		t.Fatalf("batches = %+v, want one for the org with one observation", b)
	}
	o := b[0].obs[0]
	if o.HostID != h.host || o.CheckKey != "web" || o.Item != "" || o.Metric != "http.up" ||
		!o.At.Equal(t0) || o.Value == nil || *o.Value != 1 || o.Err != "" || o.Label != nil {
		t.Fatalf("observation = %+v", o)
	}
}

func TestBlockedProbeIsNeverDoubled(t *testing.T) {
	h := newHarness(t, nil)
	h.probe.block = make(chan struct{})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", 10*time.Second))
	ctx := context.Background()
	for at := time.Duration(0); at <= 2*time.Minute; at += 5 * time.Second {
		h.clock.Set(t0.Add(at))
		h.r.tick(ctx)
	}
	close(h.probe.block)
	h.r.wg.Wait()
	if n := len(h.probe.Keys()); n != 1 {
		t.Fatalf("a probe still running was started %d times, want once", n)
	}
	if m := h.probe.MaxInFlight(); m != 1 {
		t.Fatalf("max in flight = %d, want 1", m)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	h := newHarness(t, func(_ *harness, d *Deps) { d.MaxConcurrent = 3 })
	h.probe.block = make(chan struct{})
	for i := 0; i < 10; i++ {
		h.world.addHost(h.org, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true},
			synthetic(fmt.Sprintf("c%d", i), "tcp", time.Minute))
	}
	ctx := context.Background()
	h.clock.Set(t0)
	h.r.tick(ctx)
	h.clock.Set(t0.Add(5 * time.Second))
	h.r.tick(ctx)
	if n := h.running(); n != 3 {
		t.Fatalf("started %d probes while 3 were blocked, want 3", n)
	}
	close(h.probe.block)
	h.r.wg.Wait()
	for at := 10 * time.Second; at <= 30*time.Second; at += 5 * time.Second {
		h.tickAt(at)
	}
	if n := len(h.probe.Keys()); n != 10 {
		t.Fatalf("probed %d checks once the slots freed, want all 10", n)
	}
	if m := h.probe.MaxInFlight(); m > 3 {
		t.Fatalf("max in flight = %d, want at most 3", m)
	}
}

func TestDefaultConcurrencyIs32(t *testing.T) {
	h := newHarness(t, nil)
	h.probe.block = make(chan struct{})
	for i := 0; i < 40; i++ {
		h.world.addHost(h.org, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true},
			synthetic(fmt.Sprintf("c%d", i), "tcp", time.Minute))
	}
	h.clock.Set(t0)
	h.r.tick(context.Background())
	n := h.running()
	close(h.probe.block)
	h.r.wg.Wait()
	if n != DefaultMaxConcurrent || DefaultMaxConcurrent != 32 {
		t.Fatalf("started %d probes at once, want %d (= 32)", n, DefaultMaxConcurrent)
	}
}

func TestRefreshPicksUpRemovedAndNewDefinitions(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("old", "tcp", 10*time.Second))
	h.tickAt(0)

	// The definition is replaced; until the next refresh (30 s) the old
	// set stands.
	h.world.setChecks(h.host, synthetic("new", "tcp", 10*time.Second))
	h.tickAt(10 * time.Second)
	h.tickAt(20 * time.Second)
	h.tickAt(30 * time.Second) // refresh
	before := len(h.probe.Keys())
	for at := 35 * time.Second; at <= 2*time.Minute; at += 5 * time.Second {
		h.tickAt(at)
	}
	keys := h.probe.Keys()
	for _, k := range keys[before:] {
		if k == "old" {
			t.Fatalf("a removed definition was probed after the refresh: %v", keys)
		}
	}
	if !slices.Contains(keys[:before], "new") {
		t.Fatalf("the new definition was not probed by the refresh tick: %v", keys)
	}
}

func TestUnmonitoredHostStopsBeingProbed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver func(*fakeWorld) Resolver
	}{
		{"resolver drops it", func(w *fakeWorld) Resolver { return w }},
		{"host store says so", func(w *fakeWorld) Resolver { return unmonitoredResolver{w} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(h *harness, d *Deps) { d.Resolver = tc.resolver(h.world) })
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
				synthetic("web", "tcp", 10*time.Second))
			h.tickAt(0)
			h.world.setMonitored(h.org, h.host, false)
			h.tickAt(30 * time.Second) // refresh
			n := len(h.probe.Keys())
			for at := 35 * time.Second; at <= 2*time.Minute; at += 5 * time.Second {
				h.tickAt(at)
			}
			if got := len(h.probe.Keys()); got != n {
				t.Fatalf("an unmonitored host was probed %d more times", got-n)
			}
		})
	}
}

// TestCancelDiscardsCutShortProbe is Review Focus 5: shutdown cuts a probe
// short, and what the probe reports then is not a result — Run returns
// promptly and records nothing.
func TestCancelDiscardsCutShortProbe(t *testing.T) {
	h := newHarness(t, func(_ *harness, d *Deps) { d.Tick = 10 * time.Millisecond })
	h.probe.block = make(chan struct{}) // never closed: only ctx ends the probe
	h.probe.started = make(chan string, 1)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.r.Run(ctx)
	}()
	select {
	case <-h.probe.started:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("Run never started the probe")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if b := h.rec.Batches(); len(b) != 0 {
		t.Fatalf("recorded %+v for a probe cut short by shutdown", b)
	}
	if h.locker.Unlocks() == 0 {
		t.Fatal("Run must release the lock on exit")
	}
}

// TestOwnDeadlineIsAResult: a probe that ran into its own deadline (not a
// shutdown) reports a real outcome, which is recorded.
func TestOwnDeadlineIsAResult(t *testing.T) {
	h := newHarness(t, nil)
	h.probe.block = make(chan struct{}) // never closed: only the deadline ends it
	// An interval of 1.1 s gives a 100 ms probe deadline; definitions
	// cannot be this short, the fake resolver does not care.
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", 1100*time.Millisecond))
	h.tickAt(0)
	if b := h.rec.Batches(); len(b) != 1 {
		t.Fatalf("batches = %+v, want the timed-out probe recorded", b)
	}
}

// TestProbeDeadlineBelowInterval: the probe's ctx ends one second before
// its check's interval, so the probe honours min(timeout_s, interval-1s).
func TestProbeDeadlineBelowInterval(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", 10*time.Second))
	start := time.Now()
	h.tickAt(0)
	after := time.Now()
	c := h.probe.Calls()
	if len(c) != 1 || !c[0].hasDeadline {
		t.Fatalf("calls = %+v, want one with a deadline", c)
	}
	if c[0].deadline.After(after.Add(9*time.Second)) || c[0].deadline.Before(start.Add(9*time.Second)) {
		t.Fatalf("deadline %v is not start + interval - 1s (%v .. %v)", c[0].deadline, start.Add(9*time.Second), after.Add(9*time.Second))
	}
}

func TestDefaults(t *testing.T) {
	r := NewRunner(Deps{})
	if r.tickEvery != 5*time.Second || r.refreshEvery != 30*time.Second || r.maxConcurrent != 32 ||
		r.clock == nil || r.probe == nil || r.log == nil {
		t.Fatalf("defaults = tick %v refresh %v max %d", r.tickEvery, r.refreshEvery, r.maxConcurrent)
	}
	if AdvisoryKeyProbes != 8314 {
		t.Fatalf("AdvisoryKeyProbes = %d, want 8314", AdvisoryKeyProbes)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	slices.Sort(out)
	return out
}
