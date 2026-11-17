// Phases 5 to 7 of the assistant suite: every refusal the endpoint makes,
// the secret sweep over every recorded tool output, and a token edit seen
// by an open session.
package e2e

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// initializeBody is an MCP initialize request, for the raw refusals.
var initializeBody = map[string]any{
	"jsonrpc": "2.0", "id": 1, "method": "initialize",
	"params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "e2e", "version": "1"}},
}

func (a *assistantSuite) refusals() {
	s := a.s
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	// --- An assistant token on a REST route -------------------------------
	if status, body := s.bearer(http.MethodGet, prefix+"/hosts", a.allToken, nil); status != http.StatusForbidden {
		s.t.Errorf("assistant token on GET hosts = %d %s, want 403", status, body)
	}

	// --- An api token at the assistant endpoint ---------------------------
	apiTok := s.createToken(map[string]any{"name": "asst-plain-api", "role": "owner"})
	if status, body := s.bearer(http.MethodPost, "/api/mcp", apiTok.Token, initializeBody); status != http.StatusForbidden {
		s.t.Errorf("api token at /api/mcp = %d %s, want 403", status, body)
	}
	if _, err := a.connect(apiTok.Token); err == nil {
		s.t.Error("an api token connected to /api/mcp")
	}

	// --- The switch off, then on again ------------------------------------
	s.setAssistantAccess(false)
	if status, body := s.bearer(http.MethodPost, "/api/mcp", a.allToken, initializeBody); status != http.StatusForbidden {
		s.t.Errorf("assistant token with the switch off = %d %s, want 403", status, body)
	}
	if _, err := a.connect(a.allToken); err == nil {
		s.t.Error("an assistant token connected with the switch off")
	}
	s.setAssistantAccess(true)
	cs := a.mustConnect(a.allToken)
	if len(a.listTools(cs)) != len(a.allTools) {
		s.t.Error("with the switch on again the token does not list every tool")
	}

	// --- A revoked token --------------------------------------------------
	revoked := s.createAssistantToken("asst-revoked", tenancy.AreaInventoryRead)
	_ = a.mustConnect(revoked.Token)
	s.api.do(http.MethodDelete, prefix+"/tokens/"+revoked.ID, nil, nil)
	if _, err := a.connect(revoked.Token); err == nil {
		s.t.Error("a revoked token connected")
	}

	// --- An expired token -------------------------------------------------
	expiry := time.Now().Add(2 * time.Second)
	expiring := s.createToken(map[string]any{
		"name": "asst-expiring", "role": "owner", "kind": "assistant",
		"areas": []string{string(tenancy.AreaInventoryRead)}, "expires_at": expiry.Format(time.RFC3339Nano),
	})
	if c, err := a.connect(expiring.Token); err == nil {
		_ = c.Close()
	} else if time.Now().Before(expiry) {
		s.t.Errorf("the expiring token was refused before its expiry: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := a.connect(expiring.Token)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			s.t.Fatalf("the token expiring at %s still connects at %s", expiry.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano))
		}
		time.Sleep(250 * time.Millisecond)
	}

	// --- A tool outside the token's areas ---------------------------------
	reader := s.createAssistantToken("asst-reader", tenancy.AreaInventoryRead)
	rcs := a.mustConnect(reader.Token)
	if slices.Contains(a.listTools(rcs), "create_group") {
		s.t.Error("an inventory.read token lists create_group")
	}
	if text, isErr := a.call(rcs, "create_group", map[string]any{"name": "asst_refused"}); !isErr {
		s.t.Errorf("create_group with an inventory.read token succeeded: %s", text)
	}
	var groups struct {
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	s.get(prefix+"/inventory/groups", &groups)
	for _, g := range groups.Groups {
		if g.Name == "asst_refused" {
			s.t.Error("the refused create_group created the group")
		}
	}

	// --- A secret value other than keep -----------------------------------
	// ({"$secret":"set"}, the read-back form, counts as keep by design.)
	for _, v := range []any{"e2e-asst-plain-value", map[string]any{"$secret": "e2e-asst-plain-value"}} {
		args := map[string]any{"group_id": a.seeded.secretGroupID, "vars": map[string]any{"db_password": v}}
		if text, isErr := a.call(cs, "set_group_vars", args); !isErr {
			s.t.Errorf("set_group_vars with db_password %v succeeded: %s", v, text)
		}
	}
}

// secrets is phase 6: no output of the every_tool and run_request phases
// carries a seeded secret value, while the outputs did reach the objects
// holding them.
func (a *assistantSuite) secrets() {
	s := a.s
	s.t.Helper()
	markers := a.secretMarkers()
	if len(a.seeded.pemBody) == 0 {
		s.t.Fatal("the imported key has no PEM body to look for")
	}
	reached := map[string]bool{}
	for _, o := range a.outputs {
		for _, m := range markers {
			if strings.Contains(o.text, m) {
				s.t.Errorf("%s output contains the secret %q:\n%s", o.tool, m, o.text)
			}
		}
		for _, ref := range []string{`"db_password":{"$secret":"set"}`, "asst-api", "asst-imported", "asst-vault"} {
			if strings.Contains(o.text, ref) {
				reached[ref] = true
			}
		}
	}
	for _, ref := range []string{`"db_password":{"$secret":"set"}`, "asst-api", "asst-imported", "asst-vault"} {
		if !reached[ref] {
			s.t.Errorf("no tool output shows %s: the sweep did not reach that secret's holder", ref)
		}
	}
	s.t.Logf("swept %d tool outputs for %d secret markers", len(a.outputs), len(markers))
}

// edit is phase 7: an owner's edit of a token's areas applies to its open
// session at once, a reconnect's tools/list shows an added area, the token
// cannot edit itself, and the activity log holds the edits.
func (a *assistantSuite) edit() {
	s := a.s
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	tok := s.createAssistantToken("asst-edit", tenancy.AreaMonitoringRead, tenancy.AreaAlertsHandle)
	cs := a.mustConnect(tok.Token)
	if !slices.Contains(a.listTools(cs), "acknowledge_alert") {
		s.t.Fatal("a monitoring.read + alerts.handle token does not list acknowledge_alert")
	}
	patch := func(areas ...tenancy.Area) {
		s.t.Helper()
		s.api.do(http.MethodPatch, prefix+"/tokens/"+tok.ID, map[string]any{"areas": areaStrings(areas)}, nil)
	}

	// --- Dropping alerts.handle refuses its tool on the open session ------
	patch(tenancy.AreaMonitoringRead)
	if text, isErr := a.call(cs, "acknowledge_alert", map[string]any{"alert_id": a.seeded.alertID, "note": "after the edit"}); !isErr {
		s.t.Errorf("acknowledge_alert after alerts.handle was dropped succeeded: %s", text)
	}
	var alert struct {
		Alert struct {
			AckAt *time.Time `json:"ack_at"`
		} `json:"alert"`
	}
	s.get(prefix+"/alerts/"+a.seeded.alertID, &alert)
	if alert.Alert.AckAt != nil {
		s.t.Error("the refused acknowledge_alert acknowledged the alert")
	}

	// --- Adding templates.read shows its tools on a fresh tools/list ------
	patch(tenancy.AreaMonitoringRead, tenancy.AreaTemplatesRead)
	names := a.listTools(cs)
	if !slices.Contains(names, "list_schedules") || slices.Contains(names, "acknowledge_alert") {
		s.t.Fatalf("tools/list after the edits = %v, want list_schedules and no acknowledge_alert", names)
	}
	if text, isErr := a.call(cs, "list_schedules", nil); isErr || !strings.Contains(text, a.seeded.scheduleID) {
		s.t.Errorf("list_schedules after templates.read was added = %v %s, want the seeded schedule", isErr, text)
	}

	// --- The token cannot edit itself -------------------------------------
	if status, body := s.bearer(http.MethodPatch, prefix+"/tokens/"+tok.ID, tok.Token, map[string]any{"name": "asst-edit-self"}); status != http.StatusForbidden {
		s.t.Errorf("PATCH with the assistant token itself = %d %s, want 403", status, body)
	}

	// --- The activity log holds both edits with old and new areas ---------
	var act struct {
		Items []struct {
			Summary string `json:"summary"`
		} `json:"items"`
	}
	s.get(prefix+"/activity?object_type=token", &act)
	for _, want := range []string{
		"changed assistant token asst-edit: areas monitoring.read, alerts.handle → monitoring.read",
		"changed assistant token asst-edit: areas monitoring.read → monitoring.read, templates.read",
	} {
		found := false
		for _, it := range act.Items {
			found = found || it.Summary == want
		}
		if !found {
			s.t.Errorf("activity holds no %q; token events: %+v", want, act.Items)
		}
	}
}
