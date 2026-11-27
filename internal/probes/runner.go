// Package probes is the manager's probe runner (backlog 3.13, spec
// 2026-11-11-synthetic-checks-design.md, "Where they run"): it probes the
// synthetic checks of every monitored host in every organization and
// records the results like an agent's.
//
// Exactly one manager probes at a time: the holder of the advisory lock
// AdvisoryKeyProbes, held for as long as Run runs. The schedule is in
// memory (no table): each (host, check key) is due again one interval after
// its last start, so a new holder after a restart or a failover may probe
// each check once early, and the holder never probes a check while the
// previous probe of it still runs.
package probes

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/all"
	probehttp "github.com/noixdev/pulsix/internal/probe/http"
	"github.com/noixdev/pulsix/internal/probe/snmp"
)

// AdvisoryKeyProbes is the probe runner's pg_try_advisory_lock key, next
// to schedule.AdvisoryKeyRetest (8311), the auth reaper's (8312) and the
// backup's (8313). Arbitrary but must never change.
const AdvisoryKeyProbes = int64(8314)

// Defaults for the zero values of Deps.
const (
	// DefaultTick is how often the runner looks for due checks.
	DefaultTick = 5 * time.Second
	// DefaultRefresh is how often it re-resolves every organization's
	// synthetic checks and host addresses: a definition or host change
	// takes effect within one refresh plus one tick.
	DefaultRefresh = 30 * time.Second
	// DefaultMaxConcurrent bounds the probes in flight per manager,
	// shared fairly between organizations (fairShare).
	DefaultMaxConcurrent = 32
)

// Deps is what the runner is built from. Orgs, Resolver, Hosts, Recorder
// and Locker are required; the rest default.
type Deps struct {
	Orgs     Orgs
	Resolver Resolver
	Hosts    Hosts
	Recorder Recorder
	Locker   Locker
	// Probe runs one probe of a plugin without a Poller; default the
	// all.Registry built once per runner, with Guard and one JSON gate of
	// http.DefaultJSONStages, so the http probes' JSON stages are bounded
	// across the manager.
	Probe ProbeFunc
	// NewPoller builds the Poller of one snmp slot; default snmp.NewPoller
	// on Clock, polling with Guard.
	NewPoller func() Poller
	// Guard is the dial guard the default Probe and Poller use
	// (probe.NewGuard with PULSIX_PROBE_DENY_CIDRS and
	// PULSIX_NOTIFY_NAT64_PREFIXES in production; nil is
	// probe.NewGuard(nil, nil)).
	Guard *probe.Guard
	// Secrets is the organizations' secret store. Nil fails every probe
	// whose check names a secret with "no secret store".
	Secrets Secrets
	// Agents, Groups and Vantages make agent vantage points (backlog
	// 3.13a): the runner assigns the checks with vantage groups to agents
	// and combines every vantage's results into the check's own. Without
	// all three, vantage groups are ignored and every check is probed
	// from the manager alone, as before vantages existed.
	Agents   Agents
	Groups   Groups
	Vantages VantageStore
	// Clock stamps each result's At, measures the snmp rates' intervals
	// and paces the skipped-interfaces log; default time.Now.
	Clock func() time.Time
	// Tick, Refresh and MaxConcurrent default to DefaultTick,
	// DefaultRefresh and DefaultMaxConcurrent.
	Tick, Refresh time.Duration
	MaxConcurrent int
	Log           *slog.Logger
}

// slot is one scheduled probe: a check key on a host.
type slot struct {
	host uuid.UUID
	key  string
}

// target is what probing a slot needs. secret is the name of the secret
// the check's secret parameter names, "" when its plugin takes none or the
// check names none; it is read off the params at refresh, so a probe
// that needs no secret does no lookup at all. check.Params are the
// check's params without the vantage params (checkplugins.StripVantage):
// the probe never sees those; vantage holds them read. agents is a
// vantage slot's assigned agents in combining order (by host name, then
// host id).
type target struct {
	org     uuid.UUID
	address string
	check   checks.Resolved
	secret  string
	vantage checkplugins.Vantage
	agents  []agentRef
	// assertSince is when this runner first saw the check's assertions
	// as they are (the refresh that adopted them, or took the lock): a
	// round probed before it may lack entries without blame (backlog
	// 3.87, unevaluated).
	assertSince time.Time
}

// Runner probes every monitored host's synthetic checks while it holds the
// lock. Its schedule state and the slots' pollers are owned by the Run
// goroutine; only the in-flight set, the wake channel and the
// skipped-interfaces log throttle are shared with the probe goroutines.
// A probe goroutine gets its slot's poller from the launch and uses no
// other.
type Runner struct {
	orgs      Orgs
	resolver  Resolver
	hosts     Hosts
	recorder  Recorder
	locker    Locker
	probe     ProbeFunc
	newPoller func() Poller
	secrets   Secrets
	agents    Agents
	groups    Groups
	vantages  VantageStore
	clock     func() time.Time
	log       *slog.Logger
	// describe looks a plugin's descriptor up: checkplugins.Get, swapped
	// by tests for plugins of their own.
	describe func(string) (checkplugins.Descriptor, bool)

	tickEvery, refreshEvery time.Duration
	maxConcurrent           int

	// Owned by the Run goroutine.
	held      bool
	targets   map[slot]target
	refreshed time.Time
	next      map[slot]time.Time
	pollers   map[slot]Poller
	// assignedEmpty lists the organizations whose last written assignment
	// set was empty, so an organization without vantage checks is not
	// written again every refresh. expected is each vantage slot's
	// expected vantages (sorted) as last reconciled with the per-vantage
	// rows. Both are forgotten with the lock: a new holder writes once.
	assignedEmpty map[uuid.UUID]bool
	expected      map[slot]expectedSet
	// regained marks the first refresh after the lock was lost: it takes
	// every slot's assertSince afresh, as a new holder does.
	regained bool

	// skipped paces the log of a device's skipped interfaces per slot.
	skipped *throttle

	mu       sync.Mutex
	inFlight map[slot]uuid.UUID // running slot → its organization
	// missing is, per slot, the run of distinct representative rounds
	// that left assertion entries out (unevaluated). Combines of one slot
	// never overlap, but they run on probe goroutines.
	missing map[slot]missingRun
	wg      sync.WaitGroup
	// wake carries "a probe finished" to the Run loop, which then starts
	// due probes into the freed slot at once. Buffered (1) and sent to
	// without blocking: a burst of completions is one wake-up.
	wake chan struct{}
}

// NewRunner builds the runner, filling the defaults.
func NewRunner(d Deps) *Runner {
	r := &Runner{
		orgs: d.Orgs, resolver: d.Resolver, hosts: d.Hosts, recorder: d.Recorder, locker: d.Locker,
		probe: d.Probe, newPoller: d.NewPoller, secrets: d.Secrets, clock: d.Clock, log: d.Log,
		agents: d.Agents, groups: d.Groups, vantages: d.Vantages,
		describe:  checkplugins.Get,
		tickEvery: d.Tick, refreshEvery: d.Refresh, maxConcurrent: d.MaxConcurrent,
		targets: map[slot]target{}, next: map[slot]time.Time{}, pollers: map[slot]Poller{},
		assignedEmpty: map[uuid.UUID]bool{}, expected: map[slot]expectedSet{},
		inFlight: map[slot]uuid.UUID{}, missing: map[slot]missingRun{}, skipped: newThrottle(skippedLogEvery),
		wake: make(chan struct{}, 1),
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	g := d.Guard
	if r.probe == nil {
		reg := all.Registry(probehttp.NewJSONGate(probehttp.DefaultJSONStages()))
		r.probe = func(ctx context.Context, plugin string, params map[string]any, address string, secret map[string]string) []probe.Observation {
			return reg.Run(ctx, g, plugin, params, address, secret)
		}
	}
	if r.newPoller == nil {
		clock := r.clock
		r.newPoller = func() Poller { return snmpPoller{p: snmp.NewPoller(clock), g: g} }
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.tickEvery <= 0 {
		r.tickEvery = DefaultTick
	}
	if r.refreshEvery <= 0 {
		r.refreshEvery = DefaultRefresh
	}
	if r.maxConcurrent <= 0 {
		r.maxConcurrent = DefaultMaxConcurrent
	}
	return r
}

// Run probes until ctx is cancelled: one tick at once, then one per Tick,
// and a launch round whenever a probe finishes, so MaxConcurrent bounds
// the probes in flight rather than the launches per tick.
// On return no probe is still running and the lock is released; results
// of probes the cancellation cut short are discarded.
func (r *Runner) Run(ctx context.Context) {
	defer func() {
		r.wg.Wait()
		// The locker is a no-op when it holds nothing, and the advisory
		// locker unlocks on a context detached from ctx's cancellation.
		r.locker.Unlock(ctx)
	}()
	t := time.NewTicker(r.tickEvery)
	defer t.Stop()
	r.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		case <-r.wake:
			r.launchFreed(ctx)
		}
	}
}

// tick runs one tickOnce, recovering a panic that escapes it (the
// convention of notify's Dispatcher.tickSafely and the schedule tickers):
// logged with its stack, the tick is skipped and the next one retries.
func (r *Runner) tick(ctx context.Context) {
	r.safely(ctx, "tick", func() { r.tickOnce(ctx) })
}

// safely runs f and recovers a panic in it, logging it with its stack as
// what. f's work is abandoned at the panic.
func (r *Runner) safely(ctx context.Context, what string, f func(), attrs ...any) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.ErrorContext(ctx, "probe runner: "+what+" panicked",
				append(attrs, "panic", rec, "stack", string(debug.Stack()))...)
		}
	}()
	f()
}

// tickOnce takes (or keeps) the lock, refreshes the targets when a refresh
// is due, and starts every due probe that fits under the concurrency
// bound. The lock and the refresh run under the tick's own deadline (the
// refresh interval), so a hung query cannot stall the loop while the lock
// is held; the probes it starts run on ctx, each under its own deadline.
// A panic in the refresh changes nothing (the targets are replaced only
// at its end), so the known targets are still probed.
func (r *Runner) tickOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	tickCtx, cancel := context.WithTimeout(ctx, r.refreshEvery)
	defer cancel()
	locked, err := r.locker.TryLock(tickCtx)
	if err != nil && ctx.Err() == nil {
		r.log.ErrorContext(ctx, "probe runner: acquire lock", "err", err)
	}
	if err != nil || !locked {
		r.lost()
		return // another manager probes, or the lock cannot be confirmed
	}
	r.held = true
	now := r.clock()
	if r.refreshed.IsZero() || !now.Before(r.refreshed.Add(r.refreshEvery-r.slack())) {
		r.safely(ctx, "refresh", func() {
			if err := r.refresh(tickCtx, now); err != nil && ctx.Err() == nil {
				r.log.ErrorContext(ctx, "probe runner: refresh", "err", err)
			}
		})
	}
	r.launchDue(ctx, now, r.slack())
}

// lost forgets the schedule and the slots' pollers when the lock is gone:
// while another manager holds it, that one probes, and the timing and
// counters remembered here say nothing about what it did. A regained
// lock starts afresh, like any new holder, and with a fresh refresh.
// Probes still in flight finish and are recorded; their slots are not
// started again before they do.
func (r *Runner) lost() {
	if !r.held {
		return
	}
	r.held = false
	clear(r.next)
	clear(r.pollers)
	clear(r.assignedEmpty)
	clear(r.expected)
	r.regained = true
	r.mu.Lock()
	clear(r.missing)
	r.mu.Unlock()
	r.refreshed = time.Time{}
}

// slack is how early a tick round may meet a due time: half a tick, so the
// wake-up jitter of a real ticker never pushes a probe a whole tick late.
// Two probes of one check are therefore their interval apart to within that
// jitter. A launch round after a completion uses no slack (launchFreed).
func (r *Runner) slack() time.Duration { return r.tickEvery / 2 }
