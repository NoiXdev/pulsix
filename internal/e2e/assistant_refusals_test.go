// Phases 5 to 7 of the assistant suite: every refusal the endpoint makes,
// the secret sweep over every recorded tool output, and a token edit seen
// by an open session.
package e2e

import (
	"fmt"
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

// The endpoint's and the REST layer's refusal reasons (internal/mcp/
// server.go, internal/mcp/secrets.go, internal/api/authz.go), and the MCP
// SDK's refusal of a tool the token's areas did not register.
const (
	needsAssistantToken = "this endpoint needs an assistant token"
	switchedOff         = "assistant access is turned off for this organization"
	tokenRequired       = "an assistant token is required"
	secretsByAPerson    = "secrets are set by a person in Pulsix"
	onlyAtEndpoint      = "assistant tokens work only at the assistant endpoint"
)

func unknownTool(name string) string { return `unknown tool "` + name + `"` }

// mcpInitialize posts a raw initialize to /api/mcp with token as bearer
// and returns the status and body.
func (s *scenario) mcpInitialize(token string) (int, string) {
	s.t.Helper()
	status, _, raw, err := rawRequest(bearerClient, http.MethodPost, s.api.base+"/api/mcp", http.Header{
		"Authorization": {"Bearer " + token}, "Accept": {"application/json, text/event-stream"},
	}, initializeBody)
	if err != nil {
		s.t.Fatalf("POST /api/mcp: %v", err)
	}
	return status, string(raw)
}

// expectRefusal checks a raw response for the status and reason.
func (s *scenario) expectRefusal(what string, status int, body string, wantStatus int, reason string) {
	s.t.Helper()
	if status != wantStatus || !strings.Contains(body, reason) {
		s.t.Errorf("%s = %d %s, want %d with %q", what, status, body, wantStatus, reason)
	}
}

// expectToolRefusal checks a tool call's result for an error with reason.
func (s *scenario) expectToolRefusal(what, text string, isErr bool, reason string) {
	s.t.Helper()
	if !isErr || !strings.Contains(text, reason) {
		s.t.Errorf("%s = error %v %s, want refused with %q", what, isErr, text, reason)
	}
}

func (a *assistantSuite) refusals() {
	s := a.s
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	// --- An assistant token on a REST route -------------------------------
	status, body := s.bearer(http.MethodGet, prefix+"/hosts", a.allToken, nil)
	s.expectRefusal("assistant token on GET hosts", status, body, http.StatusForbidden, onlyAtEndpoint)

	// --- An api token at the assistant endpoint ---------------------------
	apiTok := s.createToken(map[string]any{"name": "asst-plain-api", "role": "owner"})
	status, body = s.mcpInitialize(apiTok.Token)
	s.expectRefusal("api token at /api/mcp", status, body, http.StatusForbidden, needsAssistantToken)

	// --- The switch off, then on again ------------------------------------
	s.setAssistantAccess(false)
	status, body = s.mcpInitialize(a.allToken)
	s.expectRefusal("assistant token with the switch off", status, body, http.StatusForbidden, switchedOff)
	s.setAssistantAccess(true)
	cs := a.mustConnect(a.allToken)
	if len(a.listTools(cs)) != len(a.allTools) {
		s.t.Error("with the switch on again the token does not list every tool")
	}

	// --- A revoked token --------------------------------------------------
	revoked := s.createAssistantToken("asst-revoked", tenancy.AreaInventoryRead)
	_ = a.mustConnect(revoked.Token)
	s.api.do(http.MethodDelete, prefix+"/tokens/"+revoked.ID, nil, nil)
	status, body = s.mcpInitialize(revoked.Token)
	s.expectRefusal("revoked token at /api/mcp", status, body, http.StatusUnauthorized, tokenRequired)

	// --- An expired token: in before its expiry, refused after ------------
	expiry := time.Now().Add(4 * time.Second)
	expiring := s.createToken(map[string]any{
		"name": "asst-expiring", "role": "owner", "kind": "assistant",
		"areas": []string{string(tenancy.AreaInventoryRead)}, "expires_at": expiry.Format(time.RFC3339Nano),
	})
	status, body = s.mcpInitialize(expiring.Token)
	if !time.Now().Before(expiry) {
		s.t.Fatalf("creating the expiring token took past its expiry (%s): the before-expiry check could not run", expiry.Format(time.RFC3339Nano))
	}
	if status != http.StatusOK {
		s.t.Errorf("the expiring token before its expiry = %d %s, want 200", status, body)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, body = s.mcpInitialize(expiring.Token)
		if status == http.StatusUnauthorized && strings.Contains(body, tokenRequired) {
			break
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the token expiring at %s, at %s = %d %s, want 401 with %q",
				expiry.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano), status, body, tokenRequired)
		}
		time.Sleep(250 * time.Millisecond)
	}

	// --- A tool outside the token's areas ---------------------------------
	reader := s.createAssistantToken("asst-reader", tenancy.AreaInventoryRead)
	rcs := a.mustConnect(reader.Token)
	if slices.Contains(a.listTools(rcs), "create_group") {
		s.t.Error("an inventory.read token lists create_group")
	}
	text, isErr := a.call(rcs, "create_group", map[string]any{"name": "asst_refused"})
	s.expectToolRefusal("create_group with an inventory.read token", text, isErr, unknownTool("create_group"))
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
		text, isErr := a.call(cs, "set_group_vars", args)
		s.expectToolRefusal(fmt.Sprintf("set_group_vars with db_password %v", v), text, isErr, secretsByAPerson)
	}
}

// secrets is phase 6: no output of the every_tool and run_request phases
// carries a seeded secret value, while the outputs did reach the objects
// holding them.
func (a *assistantSuite) secrets() {
	s := a.s
	s.t.Helper()
	markers := a.secretMarkers()
	if len(a.seeded.pemBody) == 0 || len(a.seeded.usedKeyBody) == 0 {
		s.t.Fatal("the imported key or the key the runs use has no PEM body to look for")
	}
	// The secrets' holders by name or reference: the outputs reached them.
	// host-login is the credential the runs and connection tests log in
	// to the fixture host with, the key usedKeyBody is.
	holders := []string{`"db_password":{"$secret":"set"}`, `"member_token":{"$secret":"set"}`, "asst-api", "asst-imported", "asst-vault", "host-login"}
	reached := map[string]bool{}
	for _, o := range a.outputs {
		for _, m := range markers {
			if strings.Contains(o.text, m) {
				s.t.Errorf("%s output contains the secret %q:\n%s", o.tool, m, o.text)
			}
		}
		// Some tools indent their JSON; the holders are matched without
		// whitespace.
		compact := strings.Join(strings.Fields(o.text), "")
		for _, ref := range holders {
			if strings.Contains(compact, ref) {
				reached[ref] = true
			}
		}
	}
	for _, ref := range holders {
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
	text, isErr := a.call(cs, "acknowledge_alert", map[string]any{"alert_id": a.seeded.alertID, "note": "after the edit"})
	s.expectToolRefusal("acknowledge_alert after alerts.handle was dropped", text, isErr, unknownTool("acknowledge_alert"))
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
	status, body := s.bearer(http.MethodPatch, prefix+"/tokens/"+tok.ID, tok.Token, map[string]any{"name": "asst-edit-self"})
	s.expectRefusal("PATCH with the assistant token itself", status, body, http.StatusForbidden, onlyAtEndpoint)

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
