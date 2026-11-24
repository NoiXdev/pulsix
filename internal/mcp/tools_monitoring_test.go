package mcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

var monitoringReadTools = []string{
	"list_agents", "get_host_agent", "list_checks", "get_check", "list_host_checks", "get_host_metrics",
	"list_monitoring_groups", "get_monitoring_group", "list_alert_rules", "get_alert_rule",
	"get_host_effective_rules", "list_alerts", "get_alert", "list_maintenance_windows", "get_maintenance_window",
}

var alertHandlingTools = []string{"acknowledge_alert", "unacknowledge_alert", "snooze_alert", "unsnooze_alert"}

// ownerJSON sends a JSON request as the organization's owner (a session).
func (f orgFixture) ownerJSON(t *testing.T, method, path string, body any) []byte {
	t.Helper()
	var r *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, f.s.HTTP.URL+"/api/v1/orgs/"+f.org.String()+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.s.LoginAs(f.owner).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, buf.String())
	}
	return buf.Bytes()
}

func TestMonitoringToolsListedByArea(t *testing.T) {
	cases := []struct {
		name  string
		areas []tenancy.Area
		want  []string
		not   []string
	}{
		{"monitoring.read", []tenancy.Area{tenancy.AreaMonitoringRead}, monitoringReadTools, alertHandlingTools},
		{"alerts.handle", []tenancy.Area{tenancy.AreaAlertsHandle}, append(append([]string{}, monitoringReadTools...), alertHandlingTools...), nil},
		{"inventory.read", []tenancy.Area{tenancy.AreaInventoryRead}, nil, append(append([]string{}, monitoringReadTools...), alertHandlingTools...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrgFixture(t, tc.areas...)
			names := toolNames(t, mustConnect(t, f.s, f.token))
			for _, n := range tc.want {
				if !names[n] {
					t.Errorf("%s is not listed", n)
				}
			}
			for _, n := range tc.not {
				if names[n] {
					t.Errorf("%s is listed", n)
				}
			}
			if names["list_agent_tokens"] {
				t.Error("an enrollment-token tool is listed")
			}
		})
	}
}

func TestMonitoringReadTools(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaMonitoringRead)
	alert := f.s.SeedOpenAlertWithDeferredNotification(t, f.org)
	now := time.Now().UTC()
	var win struct {
		Window struct {
			ID string `json:"id"`
		} `json:"window"`
		ID string `json:"id"`
	}
	raw := f.ownerJSON(t, http.MethodPost, "/maintenance-windows", map[string]any{
		"name": "db upgrade", "target_kind": "host", "target_id": f.host.String(),
		"starts_at": now.Add(-time.Hour).Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339),
	})
	if err := json.Unmarshal(raw, &win); err != nil {
		t.Fatal(err)
	}
	winID := win.ID
	if winID == "" {
		winID = win.Window.ID
	}
	var grp struct {
		ID    string `json:"id"`
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := json.Unmarshal(f.ownerJSON(t, http.MethodPost, "/monitoring-groups", map[string]any{"name": "web-tier", "selector": "role=web"}), &grp); err != nil {
		t.Fatal(err)
	}
	grpID := grp.ID
	if grpID == "" {
		grpID = grp.Group.ID
	}

	obsAt := time.Now()
	v := 12.5
	if _, err := f.s.Checks.RecordObservations(f.scoped, f.org, []checks.Observation{{HostID: f.host, CheckKey: "cpu", Metric: "usage_percent", At: obsAt.Add(-time.Minute), Value: &v}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Deps.Agents.Register(f.scoped, agents.Registration{
		OrgID: f.org, HostID: f.host, AgentName: "web-1", Version: "v1.2.3", Serial: "serial-monitoring",
		NotBefore: obsAt.Add(-time.Hour), NotAfter: obsAt.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	cs := mustConnect(t, f.s, f.token)
	var checks struct {
		Checks []struct {
			ID string `json:"id"`
		} `json:"checks"`
	}
	callOK(t, cs, "list_checks", nil, &checks)
	var rules struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"rules"`
	}
	callOK(t, cs, "list_alert_rules", nil, &rules)
	if len(checks.Checks) == 0 || len(rules.Rules) == 0 {
		t.Fatalf("seeded checks %d, rules %d", len(checks.Checks), len(rules.Rules))
	}
	host := f.host.String()
	cases := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"list_agents", nil, []string{`"agents"`}},
		{"list_checks", nil, []string{checks.Checks[0].ID}},
		{"get_check", map[string]any{"check_id": checks.Checks[0].ID}, []string{checks.Checks[0].ID}},
		{"list_host_checks", map[string]any{"host_id": host}, []string{`"checks"`}},
		{"list_monitoring_groups", nil, []string{"web-tier", grpID}},
		{"get_monitoring_group", map[string]any{"monitoring_group_id": grpID}, []string{"web-tier"}},
		{"list_alert_rules", nil, []string{rules.Rules[0].ID}},
		{"list_alert_rules", map[string]any{"scope": "org"}, []string{rules.Rules[0].ID}},
		{"get_alert_rule", map[string]any{"rule_id": rules.Rules[0].ID}, []string{rules.Rules[0].ID}},
		{"get_host_effective_rules", map[string]any{"host_id": host}, []string{`"effective"`}},
		{"list_alerts", nil, []string{alert.String()}},
		{"list_alerts", map[string]any{"state": "open", "severity": "crit", "limit": 5}, []string{alert.String()}},
		{"get_alert", map[string]any{"alert_id": alert.String()}, []string{alert.String(), `"timeline"`}},
		{"list_maintenance_windows", nil, []string{"db upgrade", winID}},
		{"get_maintenance_window", map[string]any{"window_id": winID}, []string{"db upgrade"}},
	}
	for _, tc := range cases {
		text := callOK(t, cs, tc.tool, tc.args, nil)
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: result lacks %q:\n%s", tc.tool, w, text)
			}
		}
	}
	// Filters are passed through: a closed-only listing omits the open alert.
	if text := callOK(t, cs, "list_alerts", map[string]any{"state": "closed"}, nil); strings.Contains(text, alert.String()) {
		t.Errorf("state=closed still lists the open alert:\n%s", text)
	}
	// get_host_metrics passes its query through: the route answers for the
	// named check and metric with the sample the fixture recorded.
	var metrics struct {
		Check  string `json:"check"`
		Metric string `json:"metric"`
		Points []struct {
			Value float64 `json:"value"`
		} `json:"points"`
	}
	callOK(t, cs, "get_host_metrics", map[string]any{"host_id": host, "check": "cpu", "metric": "usage_percent", "range": "1h"}, &metrics)
	if metrics.Check != "cpu" || metrics.Metric != "usage_percent" || len(metrics.Points) != 1 || metrics.Points[0].Value != 12.5 {
		t.Errorf("get_host_metrics = %+v, want the one recorded cpu/usage_percent sample of 12.5", metrics)
	}
	var agent struct {
		Agent struct {
			HostID    string `json:"host_id"`
			AgentName string `json:"agent_name"`
			Version   string `json:"version"`
		} `json:"agent"`
	}
	callOK(t, cs, "get_host_agent", map[string]any{"host_id": host}, &agent)
	if agent.Agent.HostID != host || agent.Agent.AgentName != "web-1" || agent.Agent.Version != "v1.2.3" {
		t.Errorf("get_host_agent = %+v, want the registered web-1 agent v1.2.3 of host %s", agent.Agent, host)
	}
}

func TestAlertHandlingTools(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAlertsHandle)
	alert := f.s.SeedOpenAlertWithDeferredNotification(t, f.org).String()
	cs := mustConnect(t, f.s, f.token)
	id := map[string]any{"alert_id": alert}

	state := func() map[string]any {
		var out struct {
			Alert map[string]any `json:"alert"`
		}
		callOK(t, cs, "get_alert", id, &out)
		return out.Alert
	}
	ackd := func() bool {
		_, has := state()["ack_at"]
		return has
	}
	muted := func() bool {
		a := state()
		v, _ := a["muted"].(bool)
		return v
	}
	if ackd() || muted() {
		t.Fatalf("fresh alert is handled already: %v", state())
	}

	callOK(t, cs, "acknowledge_alert", map[string]any{"alert_id": alert, "note": "looking"}, nil)
	if !ackd() {
		t.Errorf("not acknowledged: %v", state())
	}
	callOK(t, cs, "unacknowledge_alert", id, nil)
	if ackd() {
		t.Errorf("still acknowledged: %v", state())
	}

	callOK(t, cs, "snooze_alert", map[string]any{"alert_id": alert, "until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, nil)
	if !muted() {
		t.Errorf("not snoozed: %v", state())
	}
	callOK(t, cs, "unsnooze_alert", id, nil)
	if muted() {
		t.Errorf("still snoozed: %v", state())
	}
	if _, isErr := call(t, cs, "snooze_alert", map[string]any{"alert_id": alert, "until_change": true, "until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}); !isErr {
		t.Error("until and until_change together were accepted")
	}

	var act struct {
		Items []struct {
			Action     string `json:"action"`
			ObjectType string `json:"object_type"`
			ObjectID   string `json:"object_id"`
			TokenName  string `json:"token_name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(f.ownerJSON(t, http.MethodGet, "/activity?limit=100", nil), &act); err != nil {
		t.Fatal(err)
	}
	// Newest first: the entries the token's calls wrote, one per successful
	// call and none for the refused snooze.
	type entry struct{ action, objectType, objectID string }
	var got []entry
	for _, it := range act.Items {
		if it.TokenName == "assistant" {
			got = append(got, entry{it.Action, it.ObjectType, it.ObjectID})
		}
	}
	want := []entry{
		{"DELETE unsnoozeAlert", "alert", alert}, {"POST snoozeAlert", "alert", alert},
		{"DELETE unacknowledgeAlert", "alert", alert}, {"POST acknowledgeAlert", "alert", alert},
	}
	if !slices.Equal(got, want) {
		t.Errorf("activity entries of the token = %+v, want %+v", got, want)
	}
}

func TestRemovedAreaRefusedOnNextCall(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAlertsHandle)
	alert := f.s.SeedOpenAlertWithDeferredNotification(t, f.org).String()
	cs := mustConnect(t, f.s, f.token) // the session lists the tools it was opened with

	var list struct {
		Tokens []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(f.ownerJSON(t, http.MethodGet, "/tokens", nil), &list); err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, tk := range list.Tokens {
		if tk.Name == "assistant" {
			id = tk.ID
		}
	}
	if id == "" {
		t.Fatalf("no assistant token in %+v", list)
	}
	callOK(t, cs, "acknowledge_alert", map[string]any{"alert_id": alert}, nil)

	f.ownerJSON(t, http.MethodPatch, "/tokens/"+id, map[string]any{"areas": []string{"monitoring.read"}})

	if text, isErr := call(t, cs, "acknowledge_alert", map[string]any{"alert_id": alert, "note": "again"}); !isErr {
		t.Errorf("acknowledge_alert still works after the area was removed: %s", text)
	}
	callOK(t, cs, "get_alert", map[string]any{"alert_id": alert}, nil)
}
