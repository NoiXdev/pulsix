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

// TestHostChecksMissingSecretIsQuietWithoutASecretPlugin: the host page's
// missing-secret warning (backlog 3.5) reads the host's agent report on
// every request. No plugin takes a secret yet, so whatever the agent
// reported — nothing at all (no agent), an unknown report, or a known
// report without any secret — no check row carries missing_secret, and
// the route keeps working for a host without an agent. The decision
// itself is internal/checks' MissingSecret, tested there against a
// test-only plugin that takes a secret.
func TestHostChecksMissingSecretIsQuietWithoutASecretPlugin(t *testing.T) {
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

	if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-host-secrets", "v1", nil, "absent"); err != nil {
		t.Fatal(err)
	}
	quiet("known report without secrets")
}
