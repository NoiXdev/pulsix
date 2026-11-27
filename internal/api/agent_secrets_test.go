package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/agents"
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
		if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-secrets-1", agents.HeartbeatReport{Version: "v0.3a", SecretNames: names, SecretsState: state}); err != nil {
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

// TestAgentDTOReportsLocalCheckProblems: the host's agent card and the
// Agents page carry the problems the agent reported about the checks
// defined in its agent.yaml (backlog 3.5a): null when unknown — an agent
// older than local checks, or one that has not reported — [] when there
// are none, the list otherwise.
func TestAgentDTOReportsLocalCheckProblems(t *testing.T) {
	f := newAgentFixture(t, "agent-local-problems")
	f.enroll(t, "agent-a", "v0.3a", "serial-local-1", true)

	expect := func(want string) {
		t.Helper()
		var body struct {
			Agent map[string]json.RawMessage `json:"agent"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+f.host.ID.String()+"/agent", nil), &body)
		if got, ok := body.Agent["local_check_problems"]; !ok || string(got) != want {
			t.Fatalf("card local_check_problems = %s (present %v), want %s", got, ok, want)
		}
		var list struct {
			Agents []map[string]json.RawMessage `json:"agents"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
		if len(list.Agents) != 1 || string(list.Agents[0]["local_check_problems"]) != want {
			t.Fatalf("list row = %v; want local_check_problems %s", list.Agents, want)
		}
	}
	heartbeat := func(state string, problems []string) {
		t.Helper()
		if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-local-1", agents.HeartbeatReport{
			Version: "v0.3a", SecretsState: state, LocalCheckProblems: problems,
		}); err != nil {
			t.Fatal(err)
		}
	}

	expect("null")

	heartbeat("ok", nil)
	expect(`[]`)

	heartbeat("ok", []string{`line 4: check "local-x": unknown plugin "nope"`})
	expect(`["line 4: check \"local-x\": unknown plugin \"nope\""]`)

	heartbeat("", []string{"stray"})
	expect("null")
}

// TestAgentDTOReportsProbesSupported: the host's agent card and the Agents
// page say whether the agent probes other hosts for the manager — false
// for an agent too old to, and for one whose agent.yaml turns it off
// (backlog 3.82).
func TestAgentDTOReportsProbesSupported(t *testing.T) {
	f := newAgentFixture(t, "agent-probes-supported")
	f.enroll(t, "agent-a", "v0.3a", "serial-probes-1", true)

	expect := func(want string) {
		t.Helper()
		var body struct {
			Agent map[string]json.RawMessage `json:"agent"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+f.host.ID.String()+"/agent", nil), &body)
		if got := string(body.Agent["probes_supported"]); got != want {
			t.Fatalf("card probes_supported = %s, want %s", got, want)
		}
		var list struct {
			Agents []map[string]json.RawMessage `json:"agents"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
		if len(list.Agents) != 1 || string(list.Agents[0]["probes_supported"]) != want {
			t.Fatalf("list row = %v; want probes_supported %s", list.Agents, want)
		}
	}
	heartbeat := func(supported bool) {
		t.Helper()
		if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-probes-1", agents.HeartbeatReport{
			Version: "v0.3a", SecretsState: "ok", ProbesSupported: supported,
		}); err != nil {
			t.Fatal(err)
		}
	}
	expect("false")
	heartbeat(true)
	expect("true")
	// An opted-out agent's heartbeat (agent.yaml probes.enabled: false).
	heartbeat(false)
	expect("false")
}

// TestAgentDTOReportsStatusPage: the host's agent card and the Agents page
// say whether the agent's local status page is on and where (backlog
// 3.132) — null for an agent that never reported it.
func TestAgentDTOReportsStatusPage(t *testing.T) {
	f := newAgentFixture(t, "agent-status-page")
	f.enroll(t, "agent-a", "v0.3a", "serial-status-1", true)

	expect := func(want string) {
		t.Helper()
		var body struct {
			Agent map[string]json.RawMessage `json:"agent"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+f.host.ID.String()+"/agent", nil), &body)
		if got := string(body.Agent["status_page"]); got != want {
			t.Fatalf("card status_page = %s, want %s", got, want)
		}
		var list struct {
			Agents []map[string]json.RawMessage `json:"agents"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
		if len(list.Agents) != 1 || string(list.Agents[0]["status_page"]) != want {
			t.Fatalf("list row = %v; want status_page %s", list.Agents, want)
		}
	}
	heartbeat := func(mode agents.StatusPageMode) {
		t.Helper()
		if err := f.s.Deps.Agents.Heartbeat(context.Background(), "serial-status-1", agents.HeartbeatReport{
			Version: "v0.3a", SecretsState: "ok", StatusPage: mode,
		}); err != nil {
			t.Fatal(err)
		}
	}
	expect("null")
	heartbeat(agents.StatusPageLoopback)
	expect(`"loopback"`)
	heartbeat(agents.StatusPageFailed)
	expect(`"failed"`)
	heartbeat("")
	expect("null")
}
