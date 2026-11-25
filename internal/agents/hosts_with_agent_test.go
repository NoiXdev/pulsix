package agents_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// HostsWithAgent names the listed hosts that run an agent (spec
// 2026-11-25 B: a run of a template with agent enrollment enrols the
// others): a host without one has no entry, nor does a host of another
// organization.
func TestHostsWithAgent(t *testing.T) {
	svc, _, _, _ := newService(t)
	org, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	a, b, none := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, a, "agent-a", "v1", "serial-a")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ctx, reg(other, b, "agent-b", "v1", "serial-b")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.HostsWithAgent(ctx, org, []uuid.UUID{a, b, none})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[a] {
		t.Fatalf("hosts with agent = %v, want only %s", got, a)
	}
	if got, err := svc.HostsWithAgent(ctx, org, nil); err != nil || len(got) != 0 {
		t.Fatalf("no hosts = %v, %v; want empty", got, err)
	}
}

// The SQL store answers the same against the real table.
func TestHostsWithAgentStore(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-hosts-with")
	withAgent := createHost(t, d, org, zone, "web-1")
	without := createHost(t, d, org, zone, "web-2")
	if err := svc.Register(ctx, registrationOf(org, withAgent.ID, "agent-a", "v1", "9601")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.HostsWithAgent(db.WithOrgScope(ctx, org), org, []uuid.UUID{withAgent.ID, without.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[withAgent.ID] {
		t.Fatalf("hosts with agent = %v, want only %s", got, withAgent.ID)
	}
}
