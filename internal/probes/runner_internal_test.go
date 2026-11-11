package probes

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
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
	if n := h.rec.AgentPathCalls(); n != 0 {
		t.Fatalf("recorded through RecordObservations %d times; probe results must use RecordProbeObservations", n)
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

// TestRegainedLockStartsAFreshSchedule: a holder that lost the lock does
// not carry its old timing over: another manager probed meanwhile, and
// the schedule it remembers says nothing about that. On regaining the
// lock it starts afresh (like any new holder, one early probe at most).
func TestRegainedLockStartsAFreshSchedule(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", 5*time.Minute))
	h.tickAt(0)
	h.locker.mu.Lock()
	h.locker.allow = false
	h.locker.mu.Unlock()
	h.tickAt(5 * time.Second)
	if n := len(h.r.next); n != 0 {
		t.Fatalf("schedule kept %d entries after the lock was lost", n)
	}
	h.locker.mu.Lock()
	h.locker.allow = true
	h.locker.mu.Unlock()
	h.tickAt(10 * time.Second)
	if n := len(h.probe.Keys()); n != 2 {
		t.Fatalf("probed %d times, want once before the loss and once on regaining the lock", n)
	}
	// Held throughout: no early probe.
	h.tickAt(15 * time.Second)
	if n := len(h.probe.Keys()); n != 2 {
		t.Fatalf("probed %d times while holding, want no probe before the interval", n)
	}
}

// blockingResolver hangs until its ctx ends, like a stuck query.
type blockingResolver struct{ sawDeadline chan bool }

func (b blockingResolver) ResolveOrg(ctx context.Context, _ uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error) {
	_, ok := ctx.Deadline()
	b.sawDeadline <- ok
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestTickHasADeadline: a hung query cannot stall the loop while the lock
// is held — the tick's work runs under a deadline (the refresh interval).
func TestTickHasADeadline(t *testing.T) {
	br := blockingResolver{sawDeadline: make(chan bool, 1)}
	h := newHarness(t, func(_ *harness, d *Deps) {
		d.Resolver = br
		d.Refresh = 50 * time.Millisecond
	})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.r.tick(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a tick blocked on a hung resolver past its deadline")
	}
	if !<-br.sawDeadline {
		t.Fatal("the resolver's ctx carried no deadline")
	}
}

// TestTimedOutRefreshKeepsTargets: a refresh cut short by the tick's
// deadline changes nothing; the known targets keep being probed.
func TestTimedOutRefreshKeepsTargets(t *testing.T) {
	var block atomic.Bool
	h := newHarness(t, func(hh *harness, d *Deps) {
		d.Resolver = switchResolver{w: hh.world, block: &block}
		d.Refresh = 100 * time.Millisecond // the tick's deadline; every fake-clock tick refreshes
	})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", 30*time.Second))
	h.tickAt(0)
	block.Store(true)
	h.tickAt(30 * time.Second) // refresh times out, the target stands
	if n := len(h.probe.Keys()); n != 2 {
		t.Fatalf("probed %d times, want the known target probed again after a timed-out refresh", n)
	}
}

// switchResolver answers from the world, or hangs until ctx ends when
// block is set.
type switchResolver struct {
	w     *fakeWorld
	block *atomic.Bool
}

func (s switchResolver) ResolveOrg(ctx context.Context, org uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error) {
	if s.block.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.w.ResolveOrg(ctx, org)
}

// TestChangedTargetIsDueAtOnce: an edit of a check (interval, params,
// plugin) or a change of the host's address takes effect on the refresh
// that sees it, not one old interval later (final review, Important #2).
func TestChangedTargetIsDueAtOnce(t *testing.T) {
	withParam := func(c checks.Resolved, k string, v any) checks.Resolved {
		p := map[string]any{}
		for kk, vv := range c.Params {
			p[kk] = vv
		}
		p[k] = v
		c.Params = p
		return c
	}
	for _, tc := range []struct {
		name    string
		before  checks.Resolved
		after   checks.Resolved
		address string
	}{
		{"interval edit", synthetic("cert", "tls", 24*time.Hour), synthetic("cert", "tls", time.Minute), "192.0.2.1"},
		{"params edit", synthetic("web", "http", time.Hour), withParam(synthetic("web", "http", time.Hour), "url", "http://{address}/health"), "192.0.2.1"},
		{"plugin edit", synthetic("x", "tcp", time.Hour), synthetic("x", "tls", time.Hour), "192.0.2.1"},
		{"address change", synthetic("ping", "icmp", time.Hour), synthetic("ping", "icmp", time.Hour), "192.0.2.99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, tc.before)
			h.tickAt(0)
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: tc.address, Monitored: true}, tc.after)
			h.tickAt(30 * time.Second) // refresh
			calls := h.probe.Calls()
			if len(calls) != 2 {
				t.Fatalf("probed %d times, want the changed target probed again on the refresh tick", len(calls))
			}
			if calls[1].plugin != tc.after.Plugin || calls[1].address != tc.address {
				t.Fatalf("second probe = %+v, want plugin %q at %q", calls[1], tc.after.Plugin, tc.address)
			}
			// The new interval governs from there.
			n := len(h.probe.Calls())
			h.tickAt(30*time.Second + tc.after.Interval)
			if got := len(h.probe.Calls()); got != n+1 {
				t.Fatalf("probed %d more times one new interval later, want 1", got-n)
			}
		})
	}
}

// TestUnchangedTargetKeepsItsSchedule: a refresh that finds the same
// target does not make it due early.
func TestUnchangedTargetKeepsItsSchedule(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Hour))
	h.tickAt(0)
	for at := 30 * time.Second; at <= 5*time.Minute; at += 30 * time.Second {
		h.tickAt(at) // each one refreshes
	}
	if n := len(h.probe.Calls()); n != 1 {
		t.Fatalf("an unchanged target was probed %d times within its interval, want once", n)
	}
}

// syncBuffer is a log sink the probe goroutines may write concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPanickingProbeIsRecovered: a panic in a probe (untrusted network
// input reaches net/http, crypto/tls and x/net/icmp) is logged with its
// stack and records nothing; the slot is released and probed again on
// schedule, and the manager keeps running (final review, Important #3).
func TestPanickingProbeIsRecovered(t *testing.T) {
	var logs syncBuffer
	var calls atomic.Int32
	h := newHarness(t, func(_ *harness, d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, nil))
		d.Probe = func(context.Context, string, map[string]any, string) []probe.Observation {
			calls.Add(1)
			panic("malformed answer")
		}
	})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	h.tickAt(0)
	if n := h.running(); n != 0 {
		t.Fatalf("%d probes still in flight after the panic", n)
	}
	if b := h.rec.Batches(); len(b) != 0 {
		t.Fatalf("recorded %+v for a panicked probe", b)
	}
	if out := logs.String(); !strings.Contains(out, "malformed answer") || !strings.Contains(out, "stack=") {
		t.Fatalf("log %q does not carry the panic and its stack", out)
	}
	h.tickAt(time.Minute)
	if n := calls.Load(); n != 2 {
		t.Fatalf("probed %d times, want the slot probed again one interval later", n)
	}
}

// panicResolver panics while panicking is set, and answers from the
// world otherwise.
type panicResolver struct {
	w         *fakeWorld
	panicking *atomic.Bool
}

func (p panicResolver) ResolveOrg(ctx context.Context, org uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error) {
	if p.panicking.Load() {
		panic("resolver bug")
	}
	return p.w.ResolveOrg(ctx, org)
}

// TestPanickingRefreshIsRecovered: a panic in the refresh is logged with
// its stack and changes nothing; the known targets keep being probed.
func TestPanickingRefreshIsRecovered(t *testing.T) {
	var logs syncBuffer
	var panicking atomic.Bool
	h := newHarness(t, func(hh *harness, d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, nil))
		d.Resolver = panicResolver{w: hh.world, panicking: &panicking}
	})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", 30*time.Second))
	h.tickAt(0)
	panicking.Store(true)
	h.tickAt(30 * time.Second) // refresh panics
	if n := len(h.probe.Keys()); n != 2 {
		t.Fatalf("probed %d times, want the known target probed again after a panicked refresh", n)
	}
	if out := logs.String(); !strings.Contains(out, "resolver bug") || !strings.Contains(out, "stack=") {
		t.Fatalf("log %q does not carry the panic and its stack", out)
	}
	panicking.Store(false)
	h.tickAt(60 * time.Second)
	if n := len(h.probe.Keys()); n != 3 {
		t.Fatalf("probed %d times, want the runner to carry on once the resolver recovers", n)
	}
}

// selectiveProbe blocks the probes whose key starts with "a" until block
// is closed (or their ctx ends) and answers every other one at once,
// reporting its key on answered.
type selectiveProbe struct {
	block    chan struct{}
	answered chan string
	mu       sync.Mutex
	keys     []string
}

func (p *selectiveProbe) Run(ctx context.Context, plugin string, params map[string]any, _ string) []probe.Observation {
	key, _ := params["key"].(string)
	p.mu.Lock()
	p.keys = append(p.keys, key)
	p.mu.Unlock()
	if strings.HasPrefix(key, "a") {
		select {
		case <-p.block:
		case <-ctx.Done():
		}
	} else {
		p.answered <- key
	}
	one := 1.0
	return []probe.Observation{{Metric: plugin + ".up", Value: &one}}
}

func (p *selectiveProbe) count(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, k := range p.keys {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// twoOrgs is a harness with a second organization, orgB.
func twoOrgs(t *testing.T, p ProbeFunc) (*harness, uuid.UUID) {
	t.Helper()
	orgB := uuid.Must(uuid.NewV7())
	h := newHarness(t, func(hh *harness, d *Deps) {
		d.Orgs = fakeOrgs{ids: []uuid.UUID{hh.org, orgB}}
		if p != nil {
			d.Probe = p
		}
	})
	return h, orgB
}

func (h *harness) addChecks(org uuid.UUID, prefix string, n int, interval time.Duration) {
	for i := 0; i < n; i++ {
		h.world.addHost(org, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true},
			synthetic(fmt.Sprintf("%s%d", prefix, i), "tcp", interval))
	}
}

// TestOrgCannotStarveAnother: org A's black-hole probes, however many,
// cannot keep org B's due probe from starting — neither at the same tick
// nor one interval later, while A's probes still hold their slots (final
// review, Important #5).
func TestOrgCannotStarveAnother(t *testing.T) {
	sp := &selectiveProbe{block: make(chan struct{}), answered: make(chan string, 8)}
	h, orgB := twoOrgs(t, sp.Run)
	h.addChecks(h.org, "a", 40, time.Minute)
	h.addChecks(orgB, "b", 1, time.Minute)
	ctx := context.Background()

	// started reports whether org B's probe ran on the tick just done.
	started := func() bool {
		select {
		case <-sp.answered:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}
	h.clock.Set(t0)
	h.r.tick(ctx)
	if !started() {
		t.Fatal("org B was not probed at t=0")
	}
	for at := 5 * time.Second; at < time.Minute; at += 5 * time.Second {
		h.clock.Set(t0.Add(at))
		h.r.tick(ctx)
	}
	h.clock.Set(t0.Add(time.Minute))
	h.r.tick(ctx)
	if !started() {
		t.Fatal("org B was not probed again at t=60s while A's probes hung")
	}
	if n := sp.count("b"); n != 2 {
		t.Fatalf("org B was probed %d times, want twice", n)
	}
	if n := h.running(); n > DefaultMaxConcurrent {
		t.Fatalf("%d probes in flight, above the bound", n)
	}
	close(sp.block)
	h.r.wg.Wait()
}

// TestBusyOrgsShareTheSlots: two organizations with more due work than
// slots get half the slots each.
func TestBusyOrgsShareTheSlots(t *testing.T) {
	h, orgB := twoOrgs(t, nil)
	h.probe.block = make(chan struct{})
	h.addChecks(h.org, "a", 40, time.Minute)
	h.addChecks(orgB, "b", 40, time.Minute)
	h.clock.Set(t0)
	h.r.tick(context.Background())
	perOrg := map[uuid.UUID]int{}
	h.r.mu.Lock()
	for s := range h.r.inFlight {
		perOrg[h.r.targets[s].org]++
	}
	h.r.mu.Unlock()
	close(h.probe.block)
	h.r.wg.Wait()
	if perOrg[h.org] != 16 || perOrg[orgB] != 16 {
		t.Fatalf("in flight per org = %v, want 16 each", perOrg)
	}
}

// TestLoneOrgUsesEverySlot: with no other organization to keep room
// for, one organization may use all the slots.
func TestLoneOrgUsesEverySlot(t *testing.T) {
	h, orgB := twoOrgs(t, nil)
	h.probe.block = make(chan struct{})
	h.addChecks(h.org, "a", 40, time.Minute)
	h.world.addHost(orgB, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true})
	h.clock.Set(t0)
	h.r.tick(context.Background())
	n := h.running()
	close(h.probe.block)
	h.r.wg.Wait()
	if n != DefaultMaxConcurrent {
		t.Fatalf("started %d probes, want all %d slots for the only organization with checks", n, DefaultMaxConcurrent)
	}
}
