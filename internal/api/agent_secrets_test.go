package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestAgentDTOReportsSecrets: the host's agent card and the Agents page
// carry the agent's reported secret names and file state. Unknown — an
// agent older than agent.yaml, or one that has not reported — is null in
// both fields, never an empty list: "no secrets" and "cannot tell" are
// different answers for the host page (backlog 3.5).
func TestAgentDTOReportsSecrets(t *testing.T) {
	f := newAgentFixture(t, "agent-secrets")
	f.enroll(t, "agent-a", "v0.3a", "serial-secrets-1", true)

	card := func() map[string]json.RawMessage {
		t.Helper()
		var body struct {
			Agent map[string]json.RawMessage `json:"agent"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+f.host.ID.String()+"/agent", nil), &body)
		return body.Agent
	}
	expect := func(names, state string) {
		t.Helper()
		got := card()
		if string(got["secret_names"]) != names || string(got["secrets_state"]) != state {
			t.Fatalf("card secret_names = %s, secrets_state = %s; want %s, %s", got["secret_names"], got["secrets_state"], names, state)
		}
		var list struct {
			Agents []map[string]json.RawMessage `json:"agents"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
		if len(list.Agents) != 1 || string(list.Agents[0]["secret_names"]) != names || string(list.Agents[0]["secrets_state"]) != state {
			t.Fatalf("list row = %v; want secret_names %s, secrets_state %s", list.Agents, names, state)
		}
	}

	// Touched but never sent a report: unknown.
	expect("null", "null")

	heartbeat := func(names []string, state string) {
		t.Helper()
		if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-secrets-1", "v0.3a", names, state); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat([]string{"mysql-prod", "api"}, "ok")
	expect(`["api","mysql-prod"]`, `"ok"`)

	heartbeat(nil, "absent")
	expect(`[]`, `"absent"`)

	heartbeat(nil, "")
	expect("null", "null")
}
