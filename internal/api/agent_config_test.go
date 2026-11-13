package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestAgentDTOReportsTheConfigCheck: the host's agent card and the Agents
// page carry the latest configuration comparison (backlog 3.13c) — what
// the agent reported, what the manager expected, and when. Before any
// comparison the three fields are omitted, never null or empty; an agent
// that has applied nothing yet reports "", which is carried as "".
func TestAgentDTOReportsTheConfigCheck(t *testing.T) {
	f := newAgentFixture(t, "agent-config")
	f.enroll(t, "agent-a", "v0.3a", "serial-config-1", true)

	rows := func() []map[string]json.RawMessage {
		t.Helper()
		var card struct {
			Agent map[string]json.RawMessage `json:"agent"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+f.host.ID.String()+"/agent", nil), &card)
		var list struct {
			Agents []map[string]json.RawMessage `json:"agents"`
		}
		decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
		if len(list.Agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(list.Agents))
		}
		return []map[string]json.RawMessage{card.Agent, list.Agents[0]}
	}

	for _, r := range rows() {
		for _, k := range []string{"config_version", "config_expected", "config_checked_at"} {
			if v, ok := r[k]; ok {
				t.Fatalf("before any comparison %s = %s, want it omitted", k, v)
			}
		}
	}

	check := func(reported, expected string) {
		t.Helper()
		if err := f.s.Deps.Agents.RecordConfigCheck(context.Background(), "serial-config-1", reported, expected); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(reported, expected string) {
		t.Helper()
		for i, r := range rows() {
			if string(r["config_version"]) != reported || string(r["config_expected"]) != expected {
				t.Fatalf("row %d: config_version = %s, config_expected = %s; want %s, %s", i, r["config_version"], r["config_expected"], reported, expected)
			}
			var at time.Time
			if err := json.Unmarshal(r["config_checked_at"], &at); err != nil || time.Since(at) > time.Minute {
				t.Fatalf("row %d: config_checked_at = %s (%v); want a recent time", i, r["config_checked_at"], err)
			}
		}
	}
	check("", "abc")
	expect(`""`, `"abc"`)
	check("abc", "abc")
	expect(`"abc"`, `"abc"`)
}
