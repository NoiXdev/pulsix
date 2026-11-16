// Package probes runs the synthetic probes the manager assigns this agent
// for other hosts' checks (backlog 3.13a, "probe vantage points"): the
// agent is one of a check's vantage points, and what it measures goes back
// as ProbeResults. It holds the current assignment set (Apply), schedules
// each slot on its own interval with the first run spread over the
// interval by a hash of the slot, runs at most MaxConcurrent probes at
// once with internal/probe's probes and the manager's default dial guard,
// and hands each round's results to a sink (Run).
//
// It is not the check scheduler (internal/agent/checks): those are the
// agent's own plugins measuring its own host, keyed by check key alone;
// these are synthetic probes of other hosts, keyed by (target host, check
// key), and the agent's check plugin registry never carries them.
package probes

import (
	"cmp"
	"context"
	"hash/fnv"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/all"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

const (
	// MaxConcurrent is how many probes run at once (the spec's 4): an
	// agent is a monitored host first, and up to 200 assignments must not
	// turn it into a load generator.
	MaxConcurrent = 4
	// maxDeadline is the safety net on one probe: its context ends after
	// min(interval, maxDeadline). The probe's own timeout_s param (or the
	// plugin's default) is applied inside the probe and is normally far
	// shorter; this only bounds a probe that would otherwise hold one of
	// the MaxConcurrent places for longer than its slot's interval.
	maxDeadline = time.Minute
	// tick is how often the runner looks for due slots while it has any.
	// Intervals are at least checkplugins.MinInterval (10 s), so a second
	// of slack is invisible in the results; a finished probe wakes the
	// runner at once so a free place is filled without waiting for it.
	tick = time.Second
)

// Ticker is the periodic-tick source the runner looks for due slots on —
// the shape of internal/agent's Ticker, so the loop's own ticker factory
// (real or, in tests, fully controlled) can be handed over directly.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Result is one observation of one probe round, stamped with the slot it
// belongs to and the round's start: every result of a round carries the
// same At.
type Result struct {
	TargetHostID uuid.UUID
	CheckKey     string
	Item         string
	Metric       string
	Value        *float64
	Label        *string
	Err          string
	At           time.Time
}

// Deps is what NewRunner wires; zero fields take the production defaults.
type Deps struct {
	// Registry is the probes by plugin name; nil is DefaultRegistry().
	Registry probe.Registry
	// Guard decides which addresses a probe may dial; nil is
	// probe.NewGuard(nil, nil), the manager's default: loopback,
	// link-local, unspecified, multicast and cloud metadata addresses
	// refused.
	Guard *probe.Guard
	// Now is the wall clock (schedules and At); nil is time.Now.
	Now func() time.Time
	// Ticker builds the runner's tick; nil is a time.Ticker.
	Ticker func(time.Duration) Ticker
	// Log is where contained panics are reported; nil is slog.Default().
	Log *slog.Logger
}

// DefaultRegistry is internal/probe/all's registry without snmp: an snmp
// check always names a secret, and checks with a secret are probed only
// from the manager, so snmp never reaches an agent (and the manager drops
// it if it did). An snmp slot is therefore an unknown plugin here and
// reports its metrics as errors.
func DefaultRegistry() probe.Registry {
	reg := all.Registry(nil)
	delete(reg, "snmp")
	return reg
}

// slotKey identifies an assigned slot.
type slotKey struct {
	target uuid.UUID
	check  string
}

// slot is one assigned probe and its schedule. spec, params, paramsErr
// and interval never change after newSlot; next and running are guarded
// by the runner's mu.
type slot struct {
	spec      probeconfig.Spec
	params    map[string]any
	paramsErr error
	interval  time.Duration
	next      time.Time
	running   bool
}

// Runner holds the current assignment set and runs it. NewRunner wires it,
// Apply replaces the set, Run drives it until its context ends.
type Runner struct {
	reg    probe.Registry
	guard  *probe.Guard
	now    func() time.Time
	ticker func(time.Duration) Ticker
	log    *slog.Logger

	mu       sync.Mutex
	slots    map[slotKey]*slot
	version  string
	inFlight int
	// wake carries "the set changed" or "a probe finished" to Run, which
	// then looks for due slots at once. Buffered (1) and sent to without
	// blocking: a burst is one wake-up.
	wake chan struct{}
	wg   sync.WaitGroup
}

// NewRunner wires a runner with d's dependencies.
func NewRunner(d Deps) *Runner {
	r := &Runner{
		reg: d.Registry, guard: d.Guard, now: d.Now, ticker: d.Ticker, log: d.Log,
		slots: map[slotKey]*slot{}, wake: make(chan struct{}, 1),
	}
	if r.reg == nil {
		r.reg = DefaultRegistry()
	}
	if r.guard == nil {
		r.guard = probe.NewGuard(nil, nil)
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.ticker == nil {
		r.ticker = func(d time.Duration) Ticker { return wallTicker{time.NewTicker(d)} }
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r
}

// Version is probeconfig.Version of the set last applied — what the
// heartbeat reports as probe_config_version — and "" before any set
// arrived.
func (r *Runner) Version() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}

// Apply replaces the assignment set with specs. A slot named again
// unchanged (same target, key, plugin, params, interval and address) keeps
// its schedule; a removed or changed slot stops — a round of it still in
// flight finishes, but its results are discarded — and a new or changed
// one starts with its first run spread over its interval. A second spec
// for a slot already named is ignored (the manager never sends one); the
// version is still that of specs as sent, so the manager does not re-send.
func (r *Runner) Apply(specs []probeconfig.Spec) {
	now := r.now()
	r.mu.Lock()
	next := make(map[slotKey]*slot, len(specs))
	for _, sp := range specs {
		k := slotKey{target: sp.TargetHostID, check: sp.CheckKey}
		if _, dup := next[k]; dup {
			continue
		}
		if old, ok := r.slots[k]; ok && old.spec == sp {
			next[k] = old
			continue
		}
		next[k] = newSlot(sp, now)
	}
	r.slots = next
	r.version = probeconfig.Version(specs)
	r.mu.Unlock()
	r.poke()
}

// newSlot builds sp's slot, its first run firstOffset after now. Its
// params are decoded once here; a failure is kept and reported on every
// round. An interval under checkplugins.MinInterval (never sent by a
// manager, which refuses one) is raised to it rather than probing in a
// tight loop.
func newSlot(sp probeconfig.Spec, now time.Time) *slot {
	interval := max(time.Duration(sp.IntervalSeconds)*time.Second, checkplugins.MinInterval)
	params, err := decodeParams(sp.ParamsJSON)
	return &slot{
		spec: sp, params: params, paramsErr: err, interval: interval,
		next: now.Add(firstOffset(sp.TargetHostID, sp.CheckKey, interval)),
	}
}

// firstRunWindow caps the window a slot's first run is spread over: a
// slot reassigned to this agent (another vantage left its group, or a new
// slot appeared) reports within a minute even with an hourly interval,
// rather than leaving its vantage without a result for up to one interval.
const firstRunWindow = time.Minute

// firstOffset spreads a slot's first run over [0, min(interval,
// firstRunWindow)) by a hash of (target host id, check key), so a new set
// does not probe everything at the same instant and a slot lands on the
// same phase on every apply.
func firstOffset(target uuid.UUID, key string, interval time.Duration) time.Duration {
	window := min(interval, firstRunWindow)
	h := fnv.New64a()
	_, _ = h.Write(target[:])
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum64() % uint64(window))
}

// poke wakes Run without blocking.
func (r *Runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run runs the assigned slots until ctx ends, handing each round's results
// to sink (from the probes' own goroutines, so sink must be safe for
// concurrent use). It ticks only while slots are assigned. On return every
// probe it started has finished: ctx's end cancels them, and a round cut
// short that way is discarded.
func (r *Runner) Run(ctx context.Context, sink func([]Result)) {
	var t Ticker
	var tc <-chan time.Time
	defer func() {
		if t != nil {
			t.Stop()
		}
		r.wg.Wait()
	}()
	for {
		switch has := r.hasSlots(); {
		case has && t == nil:
			t = r.ticker(tick)
			tc = t.C()
		case !has && t != nil:
			t.Stop()
			t, tc = nil, nil
		}
		r.dispatch(ctx, sink)
		select {
		case <-ctx.Done():
			return
		case <-tc:
		case <-r.wake:
		}
	}
}

func (r *Runner) hasSlots() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.slots) > 0
}

// dispatch starts the due slots, longest overdue first, into the free
// places of MaxConcurrent. A slot whose previous round is still running
// waits for it; one that was due while every place was taken runs as soon
// as one frees, and then keeps its phase (next = due + interval, or one
// interval from now when it fell further behind than that).
func (r *Runner) dispatch(ctx context.Context, sink func([]Result)) {
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	free := MaxConcurrent - r.inFlight
	if free <= 0 || len(r.slots) == 0 {
		// The clock is not even read: an agent without assignments does
		// nothing here at all.
		return
	}
	now := r.now()
	type due struct {
		k slotKey
		s *slot
	}
	var ready []due
	for k, s := range r.slots {
		if !s.running && !s.next.After(now) {
			ready = append(ready, due{k, s})
		}
	}
	slices.SortFunc(ready, func(a, b due) int {
		if c := a.s.next.Compare(b.s.next); c != 0 {
			return c
		}
		if c := cmp.Compare(a.k.target.String(), b.k.target.String()); c != 0 {
			return c
		}
		return cmp.Compare(a.k.check, b.k.check)
	})
	for _, d := range ready[:min(free, len(ready))] {
		d.s.running = true
		r.inFlight++
		d.s.next = d.s.next.Add(d.s.interval)
		if !d.s.next.After(now) {
			d.s.next = now.Add(d.s.interval)
		}
		r.wg.Add(1)
		go r.round(ctx, d.k, d.s, now, sink)
	}
}

// round runs one probe of s, stamped at, and hands its results to sink
// unless s is no longer the assigned slot k (removed or replaced while it
// ran) or ctx ended (shutdown: the probe did not measure the target).
func (r *Runner) round(ctx context.Context, k slotKey, s *slot, at time.Time, sink func([]Result)) {
	defer r.wg.Done()
	obs := r.safeExecute(ctx, s)
	r.mu.Lock()
	s.running = false
	r.inFlight--
	current := r.slots[k] == s
	r.mu.Unlock()
	r.poke()
	if !current || ctx.Err() != nil || len(obs) == 0 {
		return
	}
	sink(toResults(s.spec, obs, at))
}

// toResults stamps a round's observations with its slot and start.
func toResults(sp probeconfig.Spec, obs []probe.Observation, at time.Time) []Result {
	out := make([]Result, len(obs))
	for i, o := range obs {
		out[i] = Result{
			TargetHostID: sp.TargetHostID, CheckKey: sp.CheckKey,
			Item: o.Item, Metric: o.Metric, Value: o.Value, Label: o.Label, Err: o.Err, At: at,
		}
	}
	return out
}

// wallTicker is the production ticker.
type wallTicker struct{ t *time.Ticker }

func (w wallTicker) C() <-chan time.Time { return w.t.C }
func (w wallTicker) Stop()               { w.t.Stop() }
