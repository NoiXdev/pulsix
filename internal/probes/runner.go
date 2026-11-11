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

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/probe"
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
	// Probe runs one probe; default probe.Run with Guard.
	Probe ProbeFunc
	// Guard is the dial guard the default Probe uses (probe.NewGuard with
	// PULSIX_PROBE_DENY_CIDRS and PULSIX_NOTIFY_NAT64_PREFIXES in
	// production; nil is probe.NewGuard(nil, nil)).
	Guard *probe.Guard
	// Clock stamps each result's At; default time.Now.
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

// target is what probing a slot needs.
type target struct {
	org     uuid.UUID
	address string
	check   checks.Resolved
}

// Runner probes every monitored host's synthetic checks while it holds the
// lock. Its schedule state is owned by the Run goroutine; only the
// in-flight set and the wake channel are shared with the probe goroutines.
type Runner struct {
	orgs     Orgs
	resolver Resolver
	hosts    Hosts
	recorder Recorder
	locker   Locker
	probe    ProbeFunc
	clock    func() time.Time
	log      *slog.Logger

	tickEvery, refreshEvery time.Duration
	maxConcurrent           int

	// Owned by the Run goroutine.
	held      bool
	targets   map[slot]target
	refreshed time.Time
	next      map[slot]time.Time

	mu       sync.Mutex
	inFlight map[slot]uuid.UUID // running slot → its organization
	wg       sync.WaitGroup
	// wake carries "a probe finished" to the Run loop, which then starts
	// due probes into the freed slot at once. Buffered (1) and sent to
	// without blocking: a burst of completions is one wake-up.
	wake chan struct{}
}

// NewRunner builds the runner, filling the defaults.
func NewRunner(d Deps) *Runner {
	r := &Runner{
		orgs: d.Orgs, resolver: d.Resolver, hosts: d.Hosts, recorder: d.Recorder, locker: d.Locker,
		probe: d.Probe, clock: d.Clock, log: d.Log,
		tickEvery: d.Tick, refreshEvery: d.Refresh, maxConcurrent: d.MaxConcurrent,
		targets: map[slot]target{}, next: map[slot]time.Time{}, inFlight: map[slot]uuid.UUID{},
		wake: make(chan struct{}, 1),
	}
	if r.probe == nil {
		g := d.Guard
		r.probe = func(ctx context.Context, plugin string, params map[string]any, address string) []probe.Observation {
			return probe.Run(ctx, g, plugin, params, address)
		}
	}
	if r.clock == nil {
		r.clock = time.Now
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
	r.launchDue(ctx, now)
}

// lost forgets the schedule when the lock is gone: while another manager
// holds it, that one probes, and the timing remembered here says nothing
// about what it did. A regained lock starts afresh, like any new holder,
// and with a fresh refresh. Probes still in flight finish and are
// recorded; their slots are not started again before they do.
func (r *Runner) lost() {
	if !r.held {
		return
	}
	r.held = false
	clear(r.next)
	r.refreshed = time.Time{}
}

// slack is how early a due time may be met: half a tick, so the wake-up
// jitter of a real ticker never pushes a probe a whole tick late. Two
// probes of one check are therefore their interval apart to within that
// jitter.
func (r *Runner) slack() time.Duration { return r.tickEvery / 2 }

// probeTimeout caps a probe below its check's interval: interval − 1 s.
// The probe itself cuts its own timeout_s to that deadline.
func probeTimeout(interval time.Duration) time.Duration {
	if d := interval - time.Second; d > 0 {
		return d
	}
	return interval
}

// probeSlot probes one slot and records the result. A probe cut short by ctx's
// cancellation (shutdown) did not measure the target, so its observations
// are dropped; one that ran into its own deadline is a real result. A
// panic in the probe or the recording (untrusted network input reaches
// net/http, crypto/tls and x/net/icmp) is logged with its stack and
// records nothing; the slot is released and due again on schedule.
func (r *Runner) probeSlot(ctx context.Context, s slot, t target) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.inFlight, s)
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default: // a wake-up is already pending
		}
	}()
	r.safely(ctx, "probe", func() { r.probeAndRecord(ctx, s, t) }, "org", t.org, "host", s.host, "check", s.key)
}

// runProbe runs the probe under its deadline; the deferred cancel also
// runs when the probe panics.
func (r *Runner) runProbe(ctx context.Context, t target) []probe.Observation {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout(t.check.Interval))
	defer cancel()
	return r.probe(pctx, t.check.Plugin, t.check.Params, t.address)
}

// probeAndRecord is probeSlot's body.
func (r *Runner) probeAndRecord(ctx context.Context, s slot, t target) {
	obs := r.runProbe(ctx, t)
	if ctx.Err() != nil || len(obs) == 0 {
		return
	}
	at := r.clock()
	batch := make([]checks.Observation, len(obs))
	for i, o := range obs {
		batch[i] = checks.Observation{
			HostID: s.host, CheckKey: s.key, Metric: o.Metric, At: at, Value: o.Value, Err: o.Err,
		}
	}
	res, err := r.recorder.RecordProbeObservations(orgScope(ctx, t.org), t.org, batch)
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "probe runner: record", "org", t.org, "host", s.host, "check", s.key, "err", err)
		}
		return
	}
	if n := res.Rejected(); n > 0 {
		r.log.WarnContext(ctx, "probe runner: observations rejected", "org", t.org, "host", s.host, "check", s.key, "rejected", n)
	}
}
