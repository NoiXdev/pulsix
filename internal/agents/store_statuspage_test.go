package agents_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
)

// TestHeartbeatStoresStatusPage: the heartbeat stores the status page
// mode (backlog 3.132) — NULL (nil) from an agent that reports none or an
// unknown one — a renewal keeps it and a re-enrollment resets it, and the
// heartbeat answers with the organization's and host's names.
func TestHeartbeatStoresStatusPage(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-status-page")
	h := createHost(t, d, org, zone, "web-status")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-s", "v1", "9701")); err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org)
	mode := func() *string {
		t.Helper()
		a, err := svc.GetByHost(scoped, org, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a.StatusPage
	}
	if got := mode(); got != nil {
		t.Fatalf("a registered agent's status page = %q, want unknown", *got)
	}
	for _, tc := range []struct {
		sent string
		want *string
	}{
		{"loopback", ptr("loopback")},
		{"network", ptr("network")},
		{"failed", ptr("failed")},
		{"off", ptr("off")},
		{"", nil},
		{"bogus", nil},
		{"network", ptr("network")},
	} {
		names, err := svc.RecordHeartbeat(ctx, "9701", agents.HeartbeatReport{Version: "v1", StatusPage: agents.StatusPageMode(tc.sent)})
		if err != nil {
			t.Fatal(err)
		}
		if names != (agents.Names{Org: "agent-status-page", Host: "web-status"}) {
			t.Fatalf("names = %+v", names)
		}
		got := mode()
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Fatalf("sent %q: stored %v, want %v", tc.sent, derefOr(got, "<nil>"), derefOr(tc.want, "<nil>"))
		}
	}
	// A renewal keeps it, a re-enrollment resets it.
	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-s", "v1", "9702"), "9701"); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got == nil || *got != "network" {
		t.Fatalf("after renewal = %v", derefOr(got, "<nil>"))
	}
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-s", "v1", "9703")); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != nil {
		t.Fatalf("after re-enrollment = %q", *got)
	}
	// An unknown serial: no names, no error.
	if names, err := svc.RecordHeartbeat(ctx, "no-such-serial", agents.HeartbeatReport{Version: "v1"}); err != nil || names != (agents.Names{}) {
		t.Fatalf("unknown serial = %+v, %v", names, err)
	}
}

func ptr(s string) *string { return &s }
