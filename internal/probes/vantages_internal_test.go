package probes

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
)

// vantageCheck is a synthetic check probed from the given groups' agents;
// extra adds or overrides params.
func vantageCheck(key, plugin string, interval time.Duration, groups []uuid.UUID, extra map[string]any) checks.Resolved {
	gs := make([]any, len(groups))
	for i, g := range groups {
		gs[i] = g.String()
	}
	params := map[string]any{"key": key, "vantage_groups": gs}
	maps.Copy(params, extra)
	return checks.Resolved{Key: key, Plugin: plugin, Params: params, Interval: interval}
}

// fleet is a target host plus agent hosts in one group.
type fleet struct {
	group  uuid.UUID
	agents []uuid.UUID
}

// addFleet adds n agent hosts named names[i] (online, probe capable) and
// a group holding them and the target host h.host.
func (h *harness) addFleet(names ...string) fleet {
	f := fleet{group: uuid.Must(uuid.NewV7())}
	members := []uuid.UUID{h.host}
	for _, name := range names {
		id := uuid.Must(uuid.NewV7())
		h.world.addHost(h.org, hosts.Host{ID: id, Name: name, Address: "198.51.100.9", Monitored: true})
		f.agents = append(f.agents, id)
		members = append(members, id)
	}
	h.groups.set(f.group, members...)
	h.agents.set(h.org, append([]uuid.UUID{h.host}, f.agents...)...)
	return f
}

func agentSet(as []Assignment) []uuid.UUID {
	out := make([]uuid.UUID, len(as))
	for i, a := range as {
		out[i] = a.AgentHostID
	}
	return sortedIDs(out)
}

// TestManagerOnlyCheckUnchanged is Review Focus 3: a check without vantage
// groups (with or without the vantage params at their defaults) records
// exactly what the runner recorded before vantages existed, probes with
// its params as they were, and causes no per-vantage work: no group or
// agent reads, no per-vantage writes or reads, and one empty assignment
// set for the organization on the first refresh only.
func TestManagerOnlyCheckUnchanged(t *testing.T) {
	for _, extra := range []map[string]any{
		nil,
		{"vantage_manager": true, "vantage_groups": []any{}, "agents_per_group": float64(3), "fail_policy": "any"},
	} {
		h := newHarness(t, nil)
		c := synthetic("web", "tcp", time.Minute)
		maps.Copy(c.Params, extra)
		h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, c)
		for at := time.Duration(0); at <= 2*time.Minute; at += 5 * time.Second {
			h.tickAt(at)
		}
		one := 1.0
		var want []recorded
		for _, at := range []time.Duration{0, time.Minute, 2 * time.Minute} {
			want = append(want, recorded{org: h.org, obs: []checks.Observation{{
				HostID: h.host, CheckKey: "web", Metric: "tcp.up", At: t0.Add(at), Value: &one,
			}}})
		}
		if got := h.rec.Batches(); !reflect.DeepEqual(got, want) {
			t.Fatalf("recorded %+v, want %+v", got, want)
		}
		for _, call := range h.probe.Calls() {
			if !reflect.DeepEqual(call.params, map[string]any{"key": "web"}) || call.address != "192.0.2.1" || call.plugin != "tcp" {
				t.Fatalf("probe call %+v, want the check's own params", call)
			}
		}
		if r, d, n := h.vstore.Records(), h.vstore.Deletes(), h.vstore.Reads(); len(r) != 0 || len(d) != 0 || n != 0 {
			t.Fatalf("per-vantage work for a manager-only check: records %v deletes %v reads %d", r, d, n)
		}
		if rs := h.vstore.Replaces(); len(rs) != 1 || rs[0].org != h.org || len(rs[0].set) != 0 {
			t.Fatalf("assignment writes = %+v, want one empty set for the org", rs)
		}
		if n, g := h.agents.Calls(), h.groups.Gets(); n != 0 || len(g) != 0 {
			t.Fatalf("agents read %d times, groups %v, want none", n, g)
		}
	}
}

func TestAssignmentsWrittenOnRefresh(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1", "a2", "a3", "a4")
	h.agents.set(h.org, h.host, f.agents[0], f.agents[1], f.agents[2]) // a4 offline or not capable
	h.world.addHost(h.org, hosts.Host{ID: h.host, Name: "target", Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, map[string]any{"port": float64(443)}))
	h.tickAt(0)
	rs := h.vstore.Replaces()
	if len(rs) != 1 {
		t.Fatalf("assignment writes = %d, want 1", len(rs))
	}
	if got, want := agentSet(rs[0].set), sortedIDs(f.agents[:3]); !slices.Equal(got, want) {
		t.Fatalf("assigned %v, want the online agents but the target %v", got, want)
	}
	for _, a := range rs[0].set {
		if a.OrgID != h.org || a.TargetHostID != h.host || a.CheckKey != "web" || a.Plugin != "tcp" ||
			a.Interval != time.Minute || a.Address != "192.0.2.1" ||
			!reflect.DeepEqual(a.Params, map[string]any{"key": "web", "port": float64(443)}) {
			t.Fatalf("assignment = %+v", a)
		}
	}
	// An unchanged world writes nothing on the next refresh (backlog 3.79).
	h.tickAt(30 * time.Second)
	if rs := h.vstore.Replaces(); len(rs) != 1 {
		t.Fatalf("second refresh wrote %+v, want nothing for an unchanged set", rs)
	}
}

// TestAssignmentsWrittenOnlyWhenChanged is backlog 3.79 (1): a refresh
// writes the organization's set when it differs from the one last
// written — an agent leaving, a changed param — and not otherwise.
func TestAssignmentsWrittenOnlyWhenChanged(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1", "a2")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Name: "target", Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, map[string]any{"port": float64(443)}))
	h.tickAt(0)
	h.tickAt(30 * time.Second)
	if rs := h.vstore.Replaces(); len(rs) != 1 || len(rs[0].set) != 2 {
		t.Fatalf("writes = %+v, want the first set only", rs)
	}
	h.agents.set(h.org, h.host, f.agents[0]) // a2 goes offline
	h.tickAt(60 * time.Second)
	h.tickAt(90 * time.Second)
	rs := h.vstore.Replaces()
	if len(rs) != 2 || !slices.Equal(agentSet(rs[1].set), f.agents[:1]) {
		t.Fatalf("writes = %+v, want one more with a1 alone", rs)
	}
	h.world.setChecks(h.host, vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, map[string]any{"port": float64(8443)}))
	h.tickAt(120 * time.Second)
	h.tickAt(150 * time.Second)
	rs = h.vstore.Replaces()
	if len(rs) != 3 || rs[2].set[0].Params["port"] != float64(8443) {
		t.Fatalf("writes = %+v, want one more with the changed param", rs)
	}
}

// TestAssignmentsRewrittenAfterThePeriod is backlog 3.79 (1), R2: an
// unchanged set, empty or not, is written again once its last write is
// assignmentRewrite old, so a change made outside the runner heals.
func TestAssignmentsRewrittenAfterThePeriod(t *testing.T) {
	for name, groups := range map[string]bool{"assigned": true, "empty": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			f := h.addFleet("a1")
			c := synthetic("web", "tcp", time.Minute)
			if groups {
				c = vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil)
			}
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, c)
			for at := time.Duration(0); at < assignmentRewrite; at += 30 * time.Second {
				h.tickAt(at)
			}
			if rs := h.vstore.Replaces(); len(rs) != 1 {
				t.Fatalf("writes before the period = %d, want 1", len(rs))
			}
			h.tickAt(assignmentRewrite)
			h.tickAt(assignmentRewrite + 30*time.Second)
			rs := h.vstore.Replaces()
			if len(rs) != 2 || !slices.Equal(agentSet(rs[1].set), agentSet(rs[0].set)) {
				t.Fatalf("writes = %+v, want the same set once more at the period", rs)
			}
		})
	}
}

// TestAssignmentWriteFailureRetriedNextRefresh: a failed write leaves no
// record of the set, so the next refresh writes it although nothing
// changed.
func TestAssignmentWriteFailureRetriedNextRefresh(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil))
	h.tickAt(0)
	h.vstore.setErrs(errors.New("db down"), nil, nil)
	h.agents.set(h.org, h.host) // a1 goes: the write fails
	h.tickAt(30 * time.Second)
	h.vstore.setErrs(nil, nil, nil)
	h.agents.set(h.org, h.host, f.agents[0]) // back to the set last written
	h.tickAt(60 * time.Second)
	if rs := h.vstore.Replaces(); len(rs) != 2 || !slices.Equal(agentSet(rs[1].set), f.agents) {
		t.Fatalf("writes = %+v, want the set written again after the failed write", rs)
	}
}

func TestAssignmentsPerGroupAndSharedAgents(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1", "a2", "a3", "a4", "a5")
	other := uuid.Must(uuid.NewV7())
	h.groups.set(other, f.agents...)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group, other}, map[string]any{"agents_per_group": float64(2)}))
	h.tickAt(0)
	rs := h.vstore.Replaces()
	if len(rs) != 1 {
		t.Fatalf("assignment writes = %d", len(rs))
	}
	// Both groups have the same candidates, so both pick the same two.
	want := sortedIDs(byScore(h.host, "web", f.agents)[:2])
	if got := agentSet(rs[0].set); !slices.Equal(got, want) {
		t.Fatalf("assigned %v, want %v once each", got, want)
	}
	if g := h.groups.Gets(); g[f.group] != 1 || g[other] != 1 {
		t.Fatalf("group reads = %v, want each group once per refresh", g)
	}
}

func TestAssignmentsIgnoreDeletedGroup(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	gone := uuid.Must(uuid.NewV7())
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{gone, f.group}, nil))
	h.tickAt(0)
	rs := h.vstore.Replaces()
	if len(rs) != 1 || !slices.Equal(agentSet(rs[0].set), f.agents) {
		t.Fatalf("assignment writes = %+v, want the existing group's agent", rs)
	}
	if len(h.probe.Calls()) != 1 {
		t.Fatalf("probe calls = %d, want the manager's probe", len(h.probe.Calls()))
	}
}

func TestAssignmentReadFailureKeepsPreviousAssignments(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil))
	h.tickAt(0)
	h.agents.setErr(errors.New("db down"))
	h.tickAt(30 * time.Second)
	if rs := h.vstore.Replaces(); len(rs) != 1 {
		t.Fatalf("assignment writes = %d, want none after a failed resolution", len(rs))
	}
	h.tickAt(60 * time.Second)
	if n := len(h.probe.Calls()); n != 2 {
		t.Fatalf("probe calls = %d, want the slot still probed on its old target", n)
	}
	if _, ok := h.r.targets[slot{host: h.host, key: "web"}]; !ok {
		t.Fatal("the slot left the schedule after a failed resolution")
	}
}

func TestAssignmentWriteFailureKeepsPreviousTargets(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1", "a2")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, map[string]any{"agents_per_group": float64(1)}))
	h.tickAt(0)
	before := h.r.targets[slot{host: h.host, key: "web"}].agents
	h.vstore.setErrs(errors.New("db down"), nil, nil)
	h.agents.set(h.org, h.host) // every agent gone
	h.tickAt(30 * time.Second)
	if got := h.r.targets[slot{host: h.host, key: "web"}].agents; !reflect.DeepEqual(got, before) {
		t.Fatalf("expected agents %v after a failed write, want the previous %v", got, before)
	}
}

func TestVantageDueTimeRecordsManagerVantageAndCombined(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil))
	agent := f.agents[0].String()
	ms := 40.0
	h.vstore.put(h.host, "web",
		VantageRow{Vantage: agent, Metric: "tcp.up", At: t0.Add(-20 * time.Second), Value: f64(0)},
		VantageRow{Vantage: agent, Metric: "tcp.connect_ms", At: t0.Add(-20 * time.Second), Value: &ms})
	h.clock.Set(t0)
	h.tickAt(0)

	calls := h.probe.Calls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].params, map[string]any{"key": "web"}) {
		t.Fatalf("probe calls = %+v, want one with the vantage params stripped", calls)
	}
	recs := h.vstore.Records()
	if len(recs) != 1 || recs[0].vantage != VantageManager || recs[0].org != h.org || len(recs[0].obs) != 1 {
		t.Fatalf("vantage records = %+v, want the manager's probe", recs)
	}
	if o := recs[0].obs[0]; o.HostID != h.host || o.CheckKey != "web" || o.Metric != "tcp.up" || !o.At.Equal(t0) {
		t.Fatalf("manager vantage observation = %+v", o)
	}
	// Policy any: the agent is down, so the check is down and carries the
	// agent's round, re-stamped with the combining time.
	b := h.rec.Batches()
	if len(b) != 1 {
		t.Fatalf("combined batches = %d, want 1", len(b))
	}
	got := map[string]float64{}
	for _, o := range b[0].obs {
		if o.HostID != h.host || o.CheckKey != "web" || !o.At.Equal(t0) || o.Err != "" || o.Value == nil {
			t.Fatalf("combined observation = %+v", o)
		}
		got[o.Metric] = *o.Value
	}
	if !reflect.DeepEqual(got, map[string]float64{"tcp.up": 0, "tcp.connect_ms": 40}) {
		t.Fatalf("combined = %v, want the down agent's round", got)
	}
}

func f64(v float64) *float64 { return &v }

func TestVantageManagerDisabledOnlyCombines(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, map[string]any{"vantage_manager": false}))
	// A manager row left from before must not count: the manager is not
	// an expected vantage.
	h.vstore.put(h.host, "web", VantageRow{Vantage: VantageManager, Metric: "tcp.up", At: t0, Value: f64(1)})
	h.tickAt(0)
	if n := len(h.probe.Calls()); n != 0 {
		t.Fatalf("probed %d times with the manager vantage off", n)
	}
	if r := h.vstore.Records(); len(r) != 0 {
		t.Fatalf("vantage records = %+v, want none", r)
	}
	// n = 0: every tcp metric the check reports fails with the no-vantage
	// message (tcp's own FailAll since backlog 3.87: without expect, no
	// banner_match).
	b := h.rec.Batches()
	if len(b) != 1 {
		t.Fatalf("batches = %d, want the combined result", len(b))
	}
	want := []string{"tcp.up", "tcp.failure", "tcp.connect_ms"}
	if len(b[0].obs) != len(want) {
		t.Fatalf("combined = %+v, want one error per tcp metric", b[0].obs)
	}
	for i, o := range b[0].obs {
		if o.Metric != want[i] || o.Item != "" || o.Err != "no vantage point has a recent result" ||
			o.Value != nil || !o.At.Equal(t0) {
			t.Fatalf("combined observation = %+v", o)
		}
	}
	// The next due time combines again; the agent's fresh up result now
	// carries the check.
	h.vstore.put(h.host, "web", VantageRow{Vantage: f.agents[0].String(), Metric: "tcp.up", At: t0.Add(50 * time.Second), Value: f64(1)})
	for at := 5 * time.Second; at <= time.Minute; at += 5 * time.Second {
		h.tickAt(at)
	}
	b = h.rec.Batches()
	if len(b) != 2 || len(b[1].obs) != 1 || b[1].obs[0].Value == nil || *b[1].obs[0].Value != 1 || !b[1].obs[0].At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("second combine = %+v", b)
	}
}

// TestVantageNoFreshResultIsUnknown is Review Focus 4 through the runner:
// with the manager's probe failing and the agent silent past the window,
// the check reports unknown (errors), never up.
func TestVantageNoFreshResultIsUnknown(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "http", time.Minute, []uuid.UUID{f.group}, map[string]any{"vantage_manager": false}))
	h.vstore.put(h.host, "web", VantageRow{Vantage: f.agents[0].String(), Metric: "http.up", At: t0.Add(-151 * time.Second), Value: f64(1)})
	h.tickAt(0)
	b := h.rec.Batches()
	if len(b) != 1 || len(b[0].obs) == 0 {
		t.Fatalf("batches = %+v", b)
	}
	for _, o := range b[0].obs {
		if o.Value != nil || o.Err != "no vantage point has a recent result" {
			t.Fatalf("observation %+v, want an error (http's FailAll)", o)
		}
	}
}

func TestVantageRepresentativeIsFirstByHostName(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("zeta", "alpha")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, map[string]any{"vantage_manager": false}))
	at := t0.Add(-10 * time.Second)
	h.vstore.put(h.host, "web",
		VantageRow{Vantage: f.agents[0].String(), Metric: "tcp.up", At: at, Value: f64(0)},
		VantageRow{Vantage: f.agents[0].String(), Metric: "tcp.connect_ms", At: at, Value: f64(1)},
		VantageRow{Vantage: f.agents[1].String(), Metric: "tcp.up", At: at, Value: f64(0)},
		VantageRow{Vantage: f.agents[1].String(), Metric: "tcp.connect_ms", At: at, Value: f64(2)},
	)
	h.tickAt(0)
	b := h.rec.Batches()
	if len(b) != 1 {
		t.Fatalf("batches = %d", len(b))
	}
	var connect *float64
	found := false
	for _, o := range b[0].obs {
		if o.Metric == "tcp.connect_ms" {
			connect, found = o.Value, true
		}
	}
	switch {
	case !found:
		t.Fatal("no tcp.connect_ms observation was recorded")
	case connect == nil:
		t.Fatal("tcp.connect_ms was recorded without a value")
	case *connect != 2:
		t.Fatalf("representative connect_ms = %v, want alpha's (2)", *connect)
	}
	want := []agentRef{{id: f.agents[1], name: "alpha"}, {id: f.agents[0], name: "zeta"}}
	if got := h.r.targets[slot{host: h.host, key: "web"}].agents; !reflect.DeepEqual(got, want) {
		t.Fatalf("agent order = %+v, want by name %+v", got, want)
	}
}

func TestVantageCombineReadFailureRecordsNothing(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil))
	h.vstore.setErrs(nil, errors.New("db down"), nil)
	h.tickAt(0)
	if b := h.rec.Batches(); len(b) != 0 {
		t.Fatalf("recorded %+v from a failed read", b)
	}
	if r := h.vstore.Records(); len(r) != 1 {
		t.Fatalf("manager vantage records = %d, want 1 (the probe itself succeeded)", len(r))
	}
	h.vstore.setErrs(nil, nil, nil)
	h.tickAt(time.Minute)
	if b := h.rec.Batches(); len(b) != 1 {
		t.Fatalf("batches after recovery = %d, want the next due time to combine", len(b))
	}
}

func TestVantageRowsOfUnexpectedVantagesDeleted(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1", "a2")
	check := vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, check)
	keepOf := func(agents ...uuid.UUID) []string {
		out := []string{VantageManager}
		for _, a := range agents {
			out = append(out, a.String())
		}
		slices.Sort(out)
		return out
	}

	h.tickAt(0)
	d := h.vstore.Deletes()
	if len(d) != 1 || d[0].org != h.org || d[0].host != h.host || d[0].key != "web" || !slices.Equal(sorted(d[0].keep), keepOf(f.agents...)) {
		t.Fatalf("deletes on the first refresh = %+v", d)
	}
	h.tickAt(30 * time.Second)
	if n := len(h.vstore.Deletes()); n != 1 {
		t.Fatalf("deletes = %d after an unchanged refresh, want still 1", n)
	}

	h.agents.set(h.org, h.host, f.agents[1]) // a1 goes offline
	h.vstore.put(h.host, "web", VantageRow{Vantage: f.agents[0].String(), Metric: "tcp.up", At: t0, Value: f64(0)})
	h.tickAt(60 * time.Second)
	d = h.vstore.Deletes()
	if len(d) != 2 || !slices.Equal(sorted(d[1].keep), keepOf(f.agents[1])) {
		t.Fatalf("deletes after a1 left = %+v", d)
	}
	b := h.rec.Batches()
	if last := b[len(b)-1]; last.obs[0].Value == nil || *last.obs[0].Value != 1 {
		t.Fatalf("the departed agent's down row still counted: %+v", last)
	}

	// The check loses its groups: every vantage row of the slot goes, once,
	// and the slot is manager-only from then on.
	plain := synthetic("web", "tcp", time.Minute)
	h.world.setChecks(h.host, plain)
	h.tickAt(90 * time.Second)
	h.tickAt(120 * time.Second)
	d = h.vstore.Deletes()
	if len(d) != 3 || len(d[2].keep) != 0 {
		t.Fatalf("deletes after the groups went = %+v, want one with an empty keep", d)
	}
	records := len(h.vstore.Records())
	h.tickAt(180 * time.Second)
	if n := len(h.vstore.Records()); n != records {
		t.Fatalf("a manager-only slot wrote vantage rows")
	}
}

func TestVantageRowsDeletedWhenSlotGoes(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil))
	h.tickAt(0)
	h.world.setMonitored(h.org, h.host, false)
	h.tickAt(30 * time.Second)
	d := h.vstore.Deletes()
	if len(d) != 2 || d[1].host != h.host || len(d[1].keep) != 0 {
		t.Fatalf("deletes = %+v, want the gone slot's rows removed", d)
	}
	if rs := h.vstore.Replaces(); len(rs) != 2 || len(rs[1].set) != 0 {
		t.Fatalf("assignment writes = %+v, want the slot's assignments gone", rs)
	}
}

func TestVantageDeleteFailureRetriedNextRefresh(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil))
	h.vstore.setErrs(nil, nil, errors.New("db down"))
	h.tickAt(0)
	h.vstore.setErrs(nil, nil, nil)
	h.tickAt(30 * time.Second)
	if d := h.vstore.Deletes(); len(d) != 1 {
		t.Fatalf("deletes = %+v, want the failed one retried", d)
	}
}

func TestSecretCheckStaysManagerOnly(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.secrets.set(h.org, "creds", map[string]string{"user": "u"})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		vantageCheck("web", "http", time.Minute, []uuid.UUID{f.group}, map[string]any{"secret": "creds"}))
	h.tickAt(0)
	if rs := h.vstore.Replaces(); len(rs) != 1 || len(rs[0].set) != 0 {
		t.Fatalf("assignments = %+v, want none for a check with a secret", rs)
	}
	if r := h.vstore.Records(); len(r) != 0 {
		t.Fatalf("vantage records = %+v", r)
	}
	if b := h.rec.Batches(); len(b) != 1 {
		t.Fatalf("batches = %d, want the manager's own result", len(b))
	}
}

// TestCustomRequestCheckStaysManagerOnly: an http check with a method
// other than GET or HEAD, headers or a body is never assigned to an
// agent, whose earlier release could send it as a plain GET (backlog
// 3.84); the descriptors refuse the combination, this guards a row
// written past them.
func TestCustomRequestCheckStaysManagerOnly(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"POST":    {"url": "http://192.0.2.1/", "method": "POST"},
		"headers": {"url": "http://192.0.2.1/", "headers": []any{"X-A: 1"}},
		"body":    {"url": "http://192.0.2.1/", "method": "PUT", "body": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			f := h.addFleet("a1")
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
				vantageCheck("web", "http", time.Minute, []uuid.UUID{f.group}, extra))
			h.tickAt(0)
			if rs := h.vstore.Replaces(); len(rs) != 1 || len(rs[0].set) != 0 {
				t.Fatalf("assignments = %+v, want none for a custom request", rs)
			}
			if b := h.rec.Batches(); len(b) != 1 {
				t.Fatalf("batches = %d, want the manager's own result", len(b))
			}
		})
	}
}

func TestLostLockForgetsVantageState(t *testing.T) {
	for name, groups := range map[string]bool{"assigned": true, "empty": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			f := h.addFleet("a1")
			c := synthetic("web", "tcp", time.Minute)
			if groups {
				c = vantageCheck("web", "tcp", time.Minute, []uuid.UUID{f.group}, nil)
			}
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, c)
			h.tickAt(0)
			h.locker.mu.Lock()
			h.locker.allow = false
			h.locker.mu.Unlock()
			h.tickAt(5 * time.Second)
			h.locker.mu.Lock()
			h.locker.allow = true
			h.locker.mu.Unlock()
			h.tickAt(10 * time.Second)
			rs := h.vstore.Replaces()
			if len(rs) != 2 || !slices.Equal(agentSet(rs[1].set), agentSet(rs[0].set)) {
				t.Fatalf("assignment writes = %+v, want the same set written again after regaining the lock", rs)
			}
		})
	}
}
