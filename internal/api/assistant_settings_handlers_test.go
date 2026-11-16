package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func TestAssistantSettingsDefaultOffAndAdminWrite(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("asst-set@example.test", "correct horse battery")
	org := s.SeedOrg("asst-set-org", owner)
	c := s.LoginAs(owner)
	url := s.HTTP.URL + "/api/v1/orgs/asst-set-org/settings/assistant"

	read := func() bool {
		t.Helper()
		resp := requestJSON(t, c, http.MethodGet, url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Settings struct {
				AssistantEnabled bool `json:"assistant_enabled"`
			} `json:"settings"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Settings.AssistantEnabled
	}
	if read() {
		t.Fatal("assistant access is on by default")
	}
	if resp := requestJSON(t, c, http.MethodPut, url, map[string]any{"assistant_enabled": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if !read() {
		t.Fatal("assistant access is still off after the PUT")
	}

	operator := s.SeedUser("asst-set-op@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	if resp := requestJSON(t, s.LoginAs(operator), http.MethodPut, url, map[string]any{"assistant_enabled": false}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator PUT = %d, want 403", resp.StatusCode)
	}
}

func TestAssistantSettingsNotWritableByToken(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	resp := s.Put(t, tok, "/api/v1/orgs/"+org.String()+"/settings/assistant", "", map[string]any{"assistant_enabled": true})
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "interactive session required") {
		t.Fatalf("token PUT = %d %s, want 403", resp.Code, resp.Body)
	}
}

// TestAssistantSwitchOffRefusesMarkedRequests: while the switch is off, a
// request through the endpoint's mark is refused, whatever the token's
// areas (spec 2026-11-16 assistant access, "Assistant access switch").
func TestAssistantSwitchOffRefusesMarkedRequests(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithAssistantToken(t, tenancy.RoleOwner, tenancy.AreaInventoryRead)
	path := "/api/v1/orgs/" + org.String() + "/hosts"
	if resp := s.AssistantDo(t, http.MethodGet, tok, path, "", nil); resp.Code != http.StatusOK {
		t.Fatalf("with the switch on = %d %s", resp.Code, resp.Body)
	}
	s.SetAssistantEnabled(t, org, false)
	resp := s.AssistantDo(t, http.MethodGet, tok, path, "", nil)
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "assistant access is turned off for this organization") {
		t.Fatalf("with the switch off = %d %s, want 403", resp.Code, resp.Body)
	}
}

func TestAssistantSettingsFollowUps(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("asst-fu@example.test", "correct horse battery")
	org := s.SeedOrg("asst-fu-org", owner)
	c := s.LoginAs(owner)
	url := s.HTTP.URL + "/api/v1/orgs/asst-fu-org/settings/assistant"

	decode := func(resp *http.Response) bool {
		t.Helper()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d %s", resp.Request.Method, resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Settings struct {
				ApprovedRunsStartFollowUps bool `json:"approved_runs_start_follow_ups"`
			} `json:"settings"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Settings.ApprovedRunsStartFollowUps
	}
	if decode(requestJSON(t, c, http.MethodGet, url, nil)) {
		t.Fatal("follow-ups start by default")
	}
	if !decode(requestJSON(t, c, http.MethodPut, url, map[string]any{"assistant_enabled": false, "approved_runs_start_follow_ups": true})) {
		t.Fatal("PUT with the field did not turn it on")
	}
	// Omitting the field keeps the stored value.
	if !decode(requestJSON(t, c, http.MethodPut, url, map[string]any{"assistant_enabled": true})) {
		t.Fatal("PUT without the field reset it")
	}

	var summaries []string
	rows, err := s.DB.Pool().Query(t.Context(), `SELECT summary FROM audit_events WHERE action = 'PUT updateAssistantSettings' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sum string
		if err := rows.Scan(&sum); err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, sum)
	}
	if len(summaries) != 2 || !strings.Contains(summaries[0], "approved run requests start follow-ups") {
		t.Fatalf("audit summaries = %q; want the first to name the follow-up change", summaries)
	}
	if strings.Contains(summaries[1], "follow-ups") {
		t.Fatalf("unchanged follow-ups named in %q", summaries[1])
	}

	operator := s.SeedUser("asst-fu-op@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	if resp := requestJSON(t, s.LoginAs(operator), http.MethodPut, url, map[string]any{"assistant_enabled": false, "approved_runs_start_follow_ups": false}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator PUT = %d, want 403", resp.StatusCode)
	}
	tokOrg, tok := s.OrgWithToken(t)
	resp := s.Put(t, tok, "/api/v1/orgs/"+tokOrg.String()+"/settings/assistant", "", map[string]any{"assistant_enabled": true, "approved_runs_start_follow_ups": true})
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "interactive session required") {
		t.Fatalf("token PUT = %d %s, want 403", resp.Code, resp.Body)
	}
}
