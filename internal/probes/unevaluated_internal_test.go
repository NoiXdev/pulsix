package probes

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe/judge"
)

// Backlog 3.87, R12 as amended in review: an entry missing from an up
// representative round is the error judge.ErrUnevaluated only when the
// round was probed after the check's assertions last changed plus the
// push horizon (the longest an agent can take to run new params) and the
// same vantage left it out of two distinct such rounds. Otherwise it is
// left unjudged for that combine: no row at all.

// ticksTo ticks every 5 s from after `from` up to and including `to`.
func (h *harness) ticksTo(from, to time.Duration) {
	for at := from + 5*time.Second; at <= to; at += 5 * time.Second {
		h.tickAt(at)
	}
}

// assertionErrs are the item rows of the latest combined batch:
// "item metric" → error ("" for a value or label).
func (h *harness) assertionErrs(t *testing.T) map[string]string {
	t.Helper()
	b := h.rec.Batches()
	if len(b) == 0 {
		t.Fatal("nothing combined")
	}
	out := map[string]string{}
	for _, o := range b[len(b)-1].obs {
		if o.Item != "" {
			out[o.Item+" "+o.Metric] = o.Err
		}
	}
	return out
}

func agentOnlyCheck(h *harness, f fleet, plugin string, assertions ...any) {
	h.world.setChecks(h.host, vantageCheck("web", plugin, time.Minute, []uuid.UUID{f.group}, map[string]any{
		"vantage_manager": false, "port": float64(22), "url": "https://example.com/", "assertions": assertions,
	}))
}

// TestUnevaluatedNeedsTwoRoundsAfterTheChange: an agent that ignores the
// parameter (older than the plugin's assertions) is flagged on the
// second of its rounds probed after the assertions were set and the push
// horizon passed, for tcp and for http alike; the first such round leaves
// the entries unjudged.
func TestUnevaluatedNeedsTwoRoundsAfterTheChange(t *testing.T) {
	for _, plugin := range []string{"tcp", "http"} {
		t.Run(plugin, func(t *testing.T) {
			h := newHarness(t, nil)
			f := h.addFleet("a1")
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true})
			agentOnlyCheck(h, f, plugin, "fast=response_ms < 500")
			h.tickAt(0) // refresh: the assertions are known from t0 on
			agent := f.agents[0].String()

			// Past the push horizon, when every current agent runs the new
			// params.
			base := pushHorizon + time.Minute
			h.vstore.put(h.host, "web", VantageRow{Vantage: agent, Metric: plugin + ".up", At: t0.Add(base + 50*time.Second), Value: f64(1)})
			h.ticksTo(base, base+time.Minute)
			if errs := h.assertionErrs(t); len(errs) != 0 {
				t.Fatalf("first round after the horizon = %v, want the entry unjudged", errs)
			}

			h.vstore.put(h.host, "web", VantageRow{Vantage: agent, Metric: plugin + ".up", At: t0.Add(base + 110*time.Second), Value: f64(1)})
			h.ticksTo(base+time.Minute, base+2*time.Minute)
			errs := h.assertionErrs(t)
			for _, k := range []string{"fast " + plugin + ".assertion", "fast " + plugin + ".assertion_actual"} {
				if errs[k] != judge.ErrUnevaluated {
					t.Errorf("%s = %q, want %q (all %v)", k, errs[k], judge.ErrUnevaluated, errs)
				}
			}
		})
	}
}

// TestUnevaluatedNotForARoundProbedBeforeTheEdit: an operator adds an
// assertion while every agent is current; the rounds probed with the old
// params lack it, and it stays unjudged until the agent's next round has
// it — no error, however many combines see the old rounds.
func TestUnevaluatedNotForARoundProbedBeforeTheEdit(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true})
	agentOnlyCheck(h, f, "http", "a=status == 200")
	h.tickAt(0)
	agent := f.agents[0].String()
	pass := "pass"
	round := func(at time.Duration, names ...string) {
		rows := []VantageRow{{Vantage: agent, Metric: "http.up", At: t0.Add(at), Value: f64(1)}}
		for _, n := range names {
			rows = append(rows,
				VantageRow{Vantage: agent, Item: n, Metric: "http.assertion", At: t0.Add(at), Label: &pass},
				VantageRow{Vantage: agent, Item: n, Metric: "http.assertion_actual", At: t0.Add(at), Label: &pass})
		}
		h.vstore.put(h.host, "web", rows...)
	}
	round(50*time.Second, "a")
	h.ticksTo(0, time.Minute)

	// The edit lands with the refresh at t0+90s; two more rounds probed
	// before it (the agent's clock, its push not yet applied) are seen.
	round(85*time.Second, "a")
	h.ticksTo(time.Minute, 85*time.Second)
	agentOnlyCheck(h, f, "http", "a=status == 200", "b=status != 503")
	round(89*time.Second, "a")
	h.ticksTo(85*time.Second, 3*time.Minute)
	for k, e := range h.assertionErrs(t) {
		if e != "" {
			t.Errorf("%s = %q, want no error for rounds probed before the edit", k, e)
		}
	}

	// The agent's round with the new params reports both.
	round(185*time.Second, "a", "b")
	h.ticksTo(3*time.Minute, 4*time.Minute)
	errs := h.assertionErrs(t)
	if errs["b http.assertion"] != "" || len(errs) != 4 {
		t.Errorf("after the new round = %v", errs)
	}
}

// TestUnevaluatedNotForASplitRound: a round stored in two messages is
// combined once while partly stored; its second half arrives before the
// next combine, and the agent's next round is again combined partly
// stored. No two distinct rounds lacked the entry as a whole, so nothing
// is flagged.
func TestUnevaluatedNotForASplitRound(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true})
	agentOnlyCheck(h, f, "tcp", "fast=connect_ms < 500")
	h.tickAt(0)
	agent := f.agents[0].String()
	pass := "pass"
	firstHalf := func(at time.Duration) {
		h.vstore.put(h.host, "web", VantageRow{Vantage: agent, Metric: "tcp.up", At: t0.Add(at), Value: f64(1)})
	}
	secondHalf := func(at time.Duration) {
		h.vstore.put(h.host, "web",
			VantageRow{Vantage: agent, Item: "fast", Metric: "tcp.assertion", At: t0.Add(at), Label: &pass},
			VantageRow{Vantage: agent, Item: "fast", Metric: "tcp.assertion_actual", At: t0.Add(at), Label: &pass})
	}
	firstHalf(50 * time.Second)
	h.ticksTo(0, time.Minute)
	secondHalf(50 * time.Second)
	h.ticksTo(time.Minute, 2*time.Minute)
	firstHalf(170 * time.Second)
	h.ticksTo(2*time.Minute, 3*time.Minute)
	for k, e := range h.assertionErrs(t) {
		if e != "" {
			t.Errorf("%s = %q, want no error for a partly stored round", k, e)
		}
	}
}

// TestUnevaluatedNotForADownRound: a down round reports no assertion
// items and is never flagged.
func TestUnevaluatedNotForADownRound(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true})
	agentOnlyCheck(h, f, "tcp", "fast=connect_ms < 500")
	h.tickAt(0)
	agent := f.agents[0].String()
	for i, at := range []time.Duration{50 * time.Second, 110 * time.Second, 170 * time.Second} {
		h.vstore.put(h.host, "web", VantageRow{Vantage: agent, Metric: "tcp.up", At: t0.Add(at), Value: f64(0)})
		h.ticksTo(time.Duration(i)*time.Minute, time.Duration(i+1)*time.Minute)
	}
	if errs := h.assertionErrs(t); len(errs) != 0 {
		t.Errorf("down rounds = %v", errs)
	}
}

// TestUnevaluatedNotWithinThePushHorizon (review round 2, N1): a current
// agent of a 15 s check keeps running its old params until its next
// heartbeat brings the new assignment; its rounds stamped after the
// refresh but within the push horizon lack the new entry and are never
// blamed, however many there are.
func TestUnevaluatedNotWithinThePushHorizon(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true})
	h.world.setChecks(h.host, vantageCheck("web", "tcp", 15*time.Second, []uuid.UUID{f.group}, map[string]any{
		"vantage_manager": false, "port": float64(22), "assertions": []any{"b=connect_ms < 500"},
	}))
	h.tickAt(0)
	agent := f.agents[0].String()
	for at := 10 * time.Second; at < pushHorizon; at += 15 * time.Second {
		h.vstore.put(h.host, "web", VantageRow{Vantage: agent, Metric: "tcp.up", At: t0.Add(at), Value: f64(1)})
		h.ticksTo(at-10*time.Second, at+5*time.Second)
		for k, e := range h.assertionErrs(t) {
			if e != "" {
				t.Fatalf("round at %v: %s = %q, want no error within the push horizon", at, k, e)
			}
		}
	}
}

// TestUnevaluatedRestartsAfterRegainingTheLock (review round 2, N2): the
// first refresh after the lock was lost and regained takes the change
// time afresh, as a new holder does, even for unchanged assertions.
func TestUnevaluatedRestartsAfterRegainingTheLock(t *testing.T) {
	h := newHarness(t, nil)
	f := h.addFleet("a1")
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true})
	agentOnlyCheck(h, f, "tcp", "fast=connect_ms < 500")
	h.tickAt(0)
	s := slot{host: h.host, key: "web"}
	if got := h.r.targets[s].assertSince; !got.Equal(t0) {
		t.Fatalf("assertSince = %v, want the first refresh %v", got, t0)
	}
	h.locker.mu.Lock()
	h.locker.allow = false
	h.locker.mu.Unlock()
	h.tickAt(5 * time.Second)
	h.locker.mu.Lock()
	h.locker.allow = true
	h.locker.mu.Unlock()
	h.tickAt(20 * time.Minute)
	if got := h.r.targets[s].assertSince; !got.Equal(t0.Add(20 * time.Minute)) {
		t.Fatalf("assertSince = %v, want the refresh after regaining the lock", got)
	}
	// A later refresh with the same assertions keeps it.
	h.tickAt(21 * time.Minute)
	if got := h.r.targets[s].assertSince; !got.Equal(t0.Add(20 * time.Minute)) {
		t.Fatalf("assertSince = %v after a later refresh, want it kept", got)
	}
}

// TestPushHorizonCoversTheLongestOnlineWindow (review round 3, N3): the
// horizon covers an agent with the longest heartbeat staying online
// (and assigned) without a heartbeat, plus its first run and the slack.
func TestPushHorizonCoversTheLongestOnlineWindow(t *testing.T) {
	if pushHorizon != 32*time.Minute {
		t.Fatalf("pushHorizon = %v, want 3 × 10 min + 1 min + 1 min", pushHorizon)
	}
}
