package agents_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// TestProbesSupportedAndOnlineCapableAgents: probes_supported starts false,
// SetProbesSupported flips it by host (org-unscoped, like the heartbeat),
// and OnlineProbeAgents lists the organization's agents that are capable
// and online by the same rule as the Agents page, never another
// organization's.
func TestProbesSupportedAndOnlineCapableAgents(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-probes")
	fresh := createHost(t, d, org, zone, "fresh")
	stale := createHost(t, d, org, zone, "stale")
	silent := createHost(t, d, org, zone, "silent")
	plain := createHost(t, d, org, zone, "plain")
	for i, h := range []uuid.UUID{fresh.ID, stale.ID, silent.ID, plain.ID} {
		if err := svc.Register(ctx, registrationOf(org, h, "agent-"+string(rune('a'+i)), "v1", "96"+string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	scoped := db.WithOrgScope(ctx, org)
	list := func() []uuid.UUID {
		t.Helper()
		ids, err := svc.OnlineProbeAgents(scoped, org)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	var supported bool
	if err := d.Pool().QueryRow(ctx, `SELECT probes_supported FROM host_agents WHERE host_id = $1`, fresh.ID).Scan(&supported); err != nil || supported {
		t.Fatalf("a registered agent supports probes = %v (%v), want false", supported, err)
	}
	for _, h := range []uuid.UUID{fresh.ID, stale.ID, silent.ID} {
		if err := svc.SetProbesSupported(ctx, h, true); err != nil {
			t.Fatal(err)
		}
	}
	// fresh heartbeated now, stale past the window, silent never.
	if _, err := d.Pool().Exec(ctx, `UPDATE host_agents SET last_seen = now() WHERE host_id IN ($1, $2)`, fresh.ID, plain.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE host_agents SET last_seen = now() - interval '10 minutes' WHERE host_id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if got := list(); !reflect.DeepEqual(got, []uuid.UUID{fresh.ID}) {
		t.Fatalf("online capable agents = %v, want only %v", got, fresh.ID)
	}

	// A capable agent that goes unsupported leaves the list; plain joins on support.
	if err := svc.SetProbesSupported(ctx, fresh.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetProbesSupported(ctx, plain.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := list(); !slices.Equal(got, []uuid.UUID{plain.ID}) {
		t.Fatalf("after flip = %v, want only %v", got, plain.ID)
	}

	// Another organization sees none of them.
	other := uuid.Must(uuid.NewV7())
	if got, err := svc.OnlineProbeAgents(db.WithOrgScope(ctx, other), other); err != nil || len(got) != 0 {
		t.Fatalf("foreign org list = %v, %v; want none", got, err)
	}
}
