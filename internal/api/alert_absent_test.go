package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestAbsentWarningCarriesNotSeenSince (backlog 3.57): a client's absent
// warning — the host-scope alert at severity warn — reads back on the
// list and the alert page with not_seen_since, its agent's last contact,
// for the GUI's "not seen since …". The unknown host alert has none.
func TestAbsentWarningCarriesNotSeenSince(t *testing.T) {
	f := newAlertFixture(t, "alert-absent")
	ctx := context.Background()
	var seen time.Time
	if err := f.s.DB.Pool().QueryRow(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, last_seen)
		VALUES ($1, $2, 'agent', 'serial-absent', now() - interval '15 days') RETURNING last_seen`,
		f.hostA, f.orgID).Scan(&seen); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	warn := f.seedOpenAlert(t, f.hostA, alerts.Subject{HostID: f.hostA, Scope: alerts.ScopeHost}, rules.StateWarn, opened)
	unknown := f.seedOpenAlert(t, f.hostB, alerts.Subject{HostID: f.hostB, Scope: alerts.ScopeHost}, rules.StateUnknown, opened)

	resp, detail := f.getAlert(t, f.viewerC, warn)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if detail.NotSeenSince == nil || !detail.NotSeenSince.Equal(seen) {
		t.Fatalf("detail not_seen_since = %v, want %v", detail.NotSeenSince, seen)
	}

	list := decodeAlertList(t, f.viewerC, f.alertsURL("?scope=host"))
	got := map[string]*time.Time{}
	for _, a := range list.Alerts {
		got[a.ID] = a.NotSeenSince
	}
	if s := got[warn.String()]; s == nil || !s.Equal(seen) {
		t.Fatalf("list not_seen_since of the warning = %v, want %v", s, seen)
	}
	if s, ok := got[unknown.String()]; !ok || s != nil {
		t.Fatalf("list not_seen_since of the unknown host alert = %v (listed %v), want absent", s, ok)
	}
}
