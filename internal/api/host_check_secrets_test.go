package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
)

// TestHostChecksMissingSecretIsQuietWithoutASecretCheck: the host page's
// missing-secret warning (backlog 3.5) reads the host's agent report on
// every request. The fixture's checks name no secret, so whatever the agent
// reported — nothing at all (no agent), an unknown report, or a known
// report without any secret — no check row carries missing_secret, and
// the route keeps working for a host without an agent. The positive case
// is TestHostChecksCarryTheMissingSecretOfAPostgresCheck.
func TestHostChecksMissingSecretIsQuietWithoutASecretCheck(t *testing.T) {
	f := newChecksFixture(t, "host-checks-secrets")
	rows := func() []map[string]json.RawMessage {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var body struct {
			Checks []map[string]json.RawMessage `json:"checks"`
		}
		decodeInto(t, resp, &body)
		if len(body.Checks) == 0 {
			t.Fatal("the fixture's host resolves no checks")
		}
		return body.Checks
	}
	quiet := func(when string) {
		t.Helper()
		for _, c := range rows() {
			if m, ok := c["missing_secret"]; ok {
				t.Fatalf("%s: check %s carries missing_secret %s", when, c["key"], m)
			}
		}
	}

	quiet("no agent")

	ctx := db.WithOrgScope(context.Background(), f.orgID)
	now := time.Now()
	if err := f.s.Deps.Agents.Register(ctx, agents.Registration{
		OrgID: f.orgID, HostID: f.web, AgentName: "web-1", Version: "v1", Serial: "serial-host-secrets",
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	quiet("unknown report")

	if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-host-secrets", agents.HeartbeatReport{Version: "v1", SecretsState: "absent"}); err != nil {
		t.Fatal(err)
	}
	quiet("known report without secrets")
}

// TestHostChecksCarryTheMissingSecretOfAPostgresCheck is the positive case
// (backlog 3.10, precondition 3): a postgres check names a secret, the
// host's agent reported secret names without it, so the host's check row
// carries missing_secret; once the agent reports it, or reports nothing
// known, the row is quiet again.
func TestHostChecksCarryTheMissingSecretOfAPostgresCheck(t *testing.T) {
	f := newChecksFixture(t, "host-checks-pg-secret")
	f.createCheck(t, f.operatorC, map[string]any{
		"key": "pg-main", "plugin": "postgres", "interval_seconds": 60,
		"binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"secret": "db-main"},
	})

	missing := func() string {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var body struct {
			Checks []struct {
				Key           string `json:"key"`
				MissingSecret string `json:"missing_secret"`
			} `json:"checks"`
		}
		decodeInto(t, resp, &body)
		for _, c := range body.Checks {
			if c.Key == "pg-main" {
				return c.MissingSecret
			}
		}
		t.Fatalf("host checks lack pg-main: %+v", body.Checks)
		return ""
	}

	ctx := db.WithOrgScope(context.Background(), f.orgID)
	now := time.Now()
	if err := f.s.Deps.Agents.Register(ctx, agents.Registration{
		OrgID: f.orgID, HostID: f.web, AgentName: "web-1", Version: "v1", Serial: "serial-host-pg-secret",
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if got := missing(); got != "" {
		t.Fatalf("unknown report: missing_secret = %q", got)
	}
	if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-host-pg-secret", agents.HeartbeatReport{Version: "v1", SecretNames: []string{"other"}, SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	if got := missing(); got != "db-main" {
		t.Fatalf("agent reported only %q: missing_secret = %q, want db-main", "other", got)
	}
	if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-host-pg-secret", agents.HeartbeatReport{Version: "v1", SecretNames: []string{"other", "db-main"}, SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	if got := missing(); got != "" {
		t.Fatalf("agent reported the secret: missing_secret = %q", got)
	}
}

// TestHostChecksCarryTheMissingSecretOfAnSNMPCheck (backlog 3.13d): a
// check the manager probes looks its secret up in the organization's
// secret store, not in the host's agent report. The snmp check's row warns
// until the store holds the name — with no agent at all, and even when the
// agent reports a secret of that name — while a postgres check naming the
// same secret keeps reading the agent's report and ignores the store.
func TestHostChecksCarryTheMissingSecretOfAnSNMPCheck(t *testing.T) {
	f := newChecksFixture(t, "host-checks-snmp-secret")
	f.createCheck(t, f.operatorC, map[string]any{
		"key": "sw", "plugin": "snmp", "interval_seconds": 60,
		"binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"secret": "sw-core", "interfaces": true},
	})
	f.createCheck(t, f.operatorC, map[string]any{
		"key": "pg-main", "plugin": "postgres", "interval_seconds": 60,
		"binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"secret": "sw-core"},
	})

	missing := func() map[string]string {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var body struct {
			Checks []struct {
				Key           string `json:"key"`
				MissingSecret string `json:"missing_secret"`
			} `json:"checks"`
		}
		decodeInto(t, resp, &body)
		out := map[string]string{}
		for _, c := range body.Checks {
			if c.Key == "sw" || c.Key == "pg-main" {
				out[c.Key] = c.MissingSecret
			}
		}
		if len(out) != 2 {
			t.Fatalf("host checks lack sw or pg-main: %+v", body.Checks)
		}
		return out
	}
	expect := func(when, sw, pg string) {
		t.Helper()
		if got := missing(); got["sw"] != sw || got["pg-main"] != pg {
			t.Fatalf("%s: missing_secret sw = %q, pg-main = %q; want %q and %q", when, got["sw"], got["pg-main"], sw, pg)
		}
	}

	expect("no agent, empty store", "sw-core", "")

	ctx := db.WithOrgScope(context.Background(), f.orgID)
	now := time.Now()
	if err := f.s.Deps.Agents.Register(ctx, agents.Registration{
		OrgID: f.orgID, HostID: f.web, AgentName: "web-1", Version: "v1", Serial: "serial-host-snmp-secret",
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-host-snmp-secret", agents.HeartbeatReport{Version: "v1", SecretNames: []string{"sw-core"}, SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	expect("the agent reports the name, the store lacks it", "sw-core", "")

	put := requestJSON(t, f.ownerC, http.MethodPut, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/secrets/sw-core",
		map[string]any{"fields": map[string]any{"community": "not-public"}})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("put secret = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	_ = put.Body.Close()
	if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-host-snmp-secret", agents.HeartbeatReport{Version: "v1", SecretNames: []string{"other"}, SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	expect("the store holds the name, the agent does not", "", "sw-core")
}
