package probes

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
)

// fakeClock is the injected clock; tests move it by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// fakeLocker grants the lock while allow is true.
type fakeLocker struct {
	mu      sync.Mutex
	allow   bool
	unlocks int
}

func (l *fakeLocker) TryLock(context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.allow, nil
}

func (l *fakeLocker) Unlock(context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unlocks++
}

func (l *fakeLocker) Unlocks() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unlocks
}

type fakeOrgs struct{ ids []uuid.UUID }

func (f fakeOrgs) ListOrgIDs(context.Context) ([]uuid.UUID, error) { return f.ids, nil }

// fakeWorld is the resolver and the host store at once: per org, the
// hosts and their synthetic checks.
type fakeWorld struct {
	mu     sync.Mutex
	hosts  map[uuid.UUID]map[uuid.UUID]hosts.Host // org → host id → host
	checks map[uuid.UUID][]checks.Resolved        // host id → synthetic checks
}

func newFakeWorld() *fakeWorld {
	return &fakeWorld{hosts: map[uuid.UUID]map[uuid.UUID]hosts.Host{}, checks: map[uuid.UUID][]checks.Resolved{}}
}

func (w *fakeWorld) addHost(org uuid.UUID, h hosts.Host, cs ...checks.Resolved) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.hosts[org] == nil {
		w.hosts[org] = map[uuid.UUID]hosts.Host{}
	}
	h.OrgID = org
	w.hosts[org][h.ID] = h
	w.checks[h.ID] = cs
}

func (w *fakeWorld) setChecks(host uuid.UUID, cs ...checks.Resolved) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.checks[host] = cs
}

func (w *fakeWorld) setMonitored(org, host uuid.UUID, monitored bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.hosts[org][host]
	h.Monitored = monitored
	w.hosts[org][host] = h
}

// ResolveOrg mirrors checks.Service: only monitored hosts get a set.
func (w *fakeWorld) ResolveOrg(_ context.Context, org uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[uuid.UUID]checks.ResolvedSet{}
	for id, h := range w.hosts[org] {
		if !h.Monitored {
			continue
		}
		out[id] = checks.ResolvedSet{HostID: id, Synthetic: append([]checks.Resolved(nil), w.checks[id]...)}
	}
	return out, nil
}

func (w *fakeWorld) ListHostsByIDs(_ context.Context, org uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []hosts.Host
	for _, id := range ids {
		if h, ok := w.hosts[org][id]; ok {
			out = append(out, h)
		}
	}
	return out, nil
}

// unmonitoredResolver answers like a resolver that has not caught up with
// a host's monitored flag yet: every host gets its set. The runner must
// still skip a host the host store reports unmonitored.
type unmonitoredResolver struct{ w *fakeWorld }

func (u unmonitoredResolver) ResolveOrg(_ context.Context, org uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error) {
	u.w.mu.Lock()
	defer u.w.mu.Unlock()
	out := map[uuid.UUID]checks.ResolvedSet{}
	for id := range u.w.hosts[org] {
		out[id] = checks.ResolvedSet{HostID: id, Synthetic: append([]checks.Resolved(nil), u.w.checks[id]...)}
	}
	return out, nil
}

type recorded struct {
	org uuid.UUID
	obs []checks.Observation
}

type fakeRecorder struct {
	mu      sync.Mutex
	batches []recorded
	// agentPath counts calls of RecordObservations, the agent ingest
	// path, which measures the host's clock: the runner must never use it.
	agentPath int
}

func (r *fakeRecorder) RecordObservations(context.Context, uuid.UUID, []checks.Observation) (checks.Ingest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentPath++
	return checks.Ingest{}, nil
}

func (r *fakeRecorder) AgentPathCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agentPath
}

func (r *fakeRecorder) RecordProbeObservations(_ context.Context, org uuid.UUID, obs []checks.Observation) (checks.Ingest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, recorded{org: org, obs: append([]checks.Observation(nil), obs...)})
	return checks.Ingest{Accepted: len(obs)}, nil
}

func (r *fakeRecorder) Batches() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.batches...)
}

// probeCall is one call the fake probe saw. The check's key travels in
// params["key"], which only the fake reads.
type probeCall struct {
	key, plugin, address string
	deadline             time.Time
	hasDeadline          bool
}

// fakeProbe counts calls and in-flight probes; block, when set, holds every
// probe until it is closed or the probe's ctx ends.
type fakeProbe struct {
	mu       sync.Mutex
	calls    []probeCall
	inFlight int
	maxSeen  int
	block    chan struct{}
	started  chan string
}

func (p *fakeProbe) Run(ctx context.Context, plugin string, params map[string]any, address string) []probe.Observation {
	key, _ := params["key"].(string)
	dl, ok := ctx.Deadline()
	p.mu.Lock()
	p.calls = append(p.calls, probeCall{key: key, plugin: plugin, address: address, deadline: dl, hasDeadline: ok})
	p.inFlight++
	if p.inFlight > p.maxSeen {
		p.maxSeen = p.inFlight
	}
	block, started := p.block, p.started
	p.mu.Unlock()
	if started != nil {
		started <- key
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	one := 1.0
	return []probe.Observation{{Metric: plugin + ".up", Value: &one}}
}

func (p *fakeProbe) Keys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.calls))
	for i, c := range p.calls {
		out[i] = c.key
	}
	return out
}

func (p *fakeProbe) Calls() []probeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]probeCall(nil), p.calls...)
}

func (p *fakeProbe) MaxInFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxSeen
}
