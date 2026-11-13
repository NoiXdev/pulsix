package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// TestTickHandsTheHostsPresenceThrough (backlog 3.57): the ticker passes
// the loaded presence into the snapshot, and a frozen result's presence
// verdict on to the alert sink — Frozen, Absent, the agent's last contact
// and the threshold — which is everything the absent warning is decided
// from.
func TestTickHandsTheHostsPresenceThrough(t *testing.T) {
	lastSeen := tickEpoch.Add(-15 * 24 * time.Hour)
	load := cpuLoad()
	load.PreviousHost = &rules.HostState{State: rules.StateOK, Since: tickEpoch.Add(-time.Hour), EvaluatedAt: tickEpoch.Add(-time.Hour)}
	load.Presence = rules.HostPresence{
		Client: true, HasAgent: true, AgentLastSeen: lastSeen, AbsentAfter: 14 * 24 * time.Hour,
		OnlineWindow: 90 * time.Second,
	}
	store := newFakeEvalStore(load, due(hostA))
	sink := &fakeSink{}
	tk := rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	res, ok := store.appliedFor(hostA)
	if !ok {
		t.Fatal("store.Apply was never called")
	}
	if !res.Frozen || len(res.States) != 0 || len(res.Changes) != 0 {
		t.Fatalf("applied %+v, want a frozen result that judged nothing", res)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("sink called %d times, want once", len(sink.calls))
	}
	in := sink.calls[0]
	if !in.Frozen || !in.Absent || !in.AgentLastSeen.Equal(lastSeen) || in.AbsentAfter != 14*24*time.Hour {
		t.Fatalf("sink saw frozen=%v absent=%v last seen=%v absent after=%v; want true, true, %v, 336h",
			in.Frozen, in.Absent, in.AgentLastSeen, in.AbsentAfter, lastSeen)
	}
	if in.Host.State != rules.StateOK || len(in.States) != 0 || len(in.Changes) != 0 || len(in.Unmeasured) != 0 {
		t.Fatalf("sink saw %+v, want the carried roll-up and nothing judged", in)
	}

	// Online again: judged, and nothing frozen reaches the sink.
	onlineSince := tickEpoch.Add(-time.Hour)
	load.Presence.AgentOnline = true
	load.Presence.AgentOnlineSince = onlineSince
	store = newFakeEvalStore(load, due(hostA))
	sink = &fakeSink{}
	tk = rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if in := sink.calls[0]; in.Frozen || in.Absent || !in.AgentLastSeen.IsZero() || in.Host.State != rules.StateCrit {
		t.Fatalf("online client: sink saw %+v, want it judged (crit) and not frozen", in)
	}
	// The unknown host alert's grace is the online window, for a client
	// with an agent only (alerts.Reconcile's shutdown-race guard),
	// measured from no earlier than the online period's start; and the
	// sink is told outright that this is a client with an agent.
	if got := sink.calls[0]; got.UnknownGrace != 90*time.Second || !got.ClientWithAgent || !got.AgentOnlineSince.Equal(onlineSince) {
		t.Fatalf("online client: sink saw grace %v, client with agent %v, online since %v; want 1m30s, true, %v",
			got.UnknownGrace, got.ClientWithAgent, got.AgentOnlineSince, onlineSince)
	}

	// A server — even one carrying a window — gets none: the host alert
	// opens the moment its roll-up reads unknown, as before.
	load.Presence.Client = false
	store = newFakeEvalStore(load, due(hostA))
	sink = &fakeSink{}
	tk = rules.NewTicker(store, sink, rules.WithTickerClock(func() time.Time { return tickEpoch }))
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := sink.calls[0]; got.UnknownGrace != 0 || got.ClientWithAgent || !got.AgentOnlineSince.IsZero() {
		t.Fatalf("server: sink saw grace %v, client with agent %v, online since %v; want none of them",
			got.UnknownGrace, got.ClientWithAgent, got.AgentOnlineSince)
	}
}
