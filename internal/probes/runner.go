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
	"cmp"
	"context"
	"log/slog"
	"slices"
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
	// DefaultMaxConcurrent bounds the probes in flight per manager.
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
	// PULSIX_PROBE_DENY_CIDRS in production; nil is probe.NewGuard(nil)).
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
// in-flight set is shared with the probe goroutines.
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
	targets   map[slot]target
	refreshed time.Time
	next      map[slot]time.Time

	mu       sync.Mutex
	inFlight map[slot]struct{}
	wg       sync.WaitGroup
}

// NewRunner builds the runner, filling the defaults.
func NewRunner(d Deps) *Runner {
	r := &Runner{
		orgs: d.Orgs, resolver: d.Resolver, hosts: d.Hosts, recorder: d.Recorder, locker: d.Locker,
		probe: d.Probe, clock: d.Clock, log: d.Log,
		tickEvery: d.Tick, refreshEvery: d.Refresh, maxConcurrent: d.MaxConcurrent,
		targets: map[slot]target{}, next: map[slot]time.Time{}, inFlight: map[slot]struct{}{},
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

// Run probes until ctx is cancelled: one tick at once, then one per Tick.
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
	for {
		r.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick takes (or keeps) the lock, refreshes the targets when a refresh is
// due, and starts every due probe that fits under the concurrency bound.
func (r *Runner) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	locked, err := r.locker.TryLock(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "probe runner: acquire lock", "err", err)
		}
		return
	}
	if !locked {
		return // another manager probes
	}
	now := r.clock()
	if r.refreshed.IsZero() || !now.Before(r.refreshed.Add(r.refreshEvery-r.slack())) {
		r.refresh(ctx, now)
	}
	r.launchDue(ctx, now)
}

// slack is how early a due time may be met: half a tick, so the wake-up
// jitter of a real ticker never pushes a probe a whole tick late. Two
// probes of one check are therefore their interval apart to within that
// jitter.
func (r *Runner) slack() time.Duration { return r.tickEvery / 2 }

// launchDue starts every due slot that is not in flight, oldest due first,
// until MaxConcurrent probes run.
func (r *Runner) launchDue(ctx context.Context, now time.Time) {
	r.mu.Lock()
	running := len(r.inFlight)
	var due []slot
	for s := range r.targets {
		if _, busy := r.inFlight[s]; busy {
			continue
		}
		if n, ok := r.next[s]; ok && now.Before(n.Add(-r.slack())) {
			continue
		}
		due = append(due, s)
	}
	r.mu.Unlock()

	slices.SortFunc(due, func(a, b slot) int {
		if c := r.next[a].Compare(r.next[b]); c != 0 {
			return c
		}
		if c := cmp.Compare(a.host.String(), b.host.String()); c != 0 {
			return c
		}
		return cmp.Compare(a.key, b.key)
	})
	for _, s := range due {
		if running >= r.maxConcurrent {
			return // the rest stay due for the next tick
		}
		running++
		t := r.targets[s]
		r.next[s] = now.Add(t.check.Interval)
		r.mu.Lock()
		r.inFlight[s] = struct{}{}
		r.mu.Unlock()
		r.wg.Add(1)
		go r.probeSlot(ctx, s, t)
	}
}

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
// are dropped; one that ran into its own deadline is a real result.
func (r *Runner) probeSlot(ctx context.Context, s slot, t target) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.inFlight, s)
		r.mu.Unlock()
	}()
	pctx, cancel := context.WithTimeout(ctx, probeTimeout(t.check.Interval))
	obs := r.probe(pctx, t.check.Plugin, t.check.Params, t.address)
	cancel()
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
	res, err := r.recorder.RecordObservations(orgScope(ctx, t.org), t.org, batch)
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
