package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// assistantSettingsFx is an owner's session on one organization's assistant
// settings.
type assistantSettingsFx struct {
	s   *apitest.Server
	c   *http.Client
	url string
}

func newAssistantSettingsFx(t *testing.T, slug string) assistantSettingsFx {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"@example.test", "correct horse battery")
	s.SeedOrg(slug, owner)
	return assistantSettingsFx{s: s, c: s.LoginAs(owner), url: s.HTTP.URL + "/api/v1/orgs/" + slug + "/settings/assistant"}
}

type assistantSettingsBody struct {
	Settings struct {
		AssistantEnabled           bool `json:"assistant_enabled"`
		ApprovedRunsStartFollowUps bool `json:"approved_runs_start_follow_ups"`
	} `json:"settings"`
}

// put sends body and returns the settings the response reports.
func (f assistantSettingsFx) put(t *testing.T, body map[string]any) (access, followUps bool) {
	t.Helper()
	resp := requestJSON(t, f.c, http.MethodPut, f.url, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %v = %d %s", body, resp.StatusCode, mustReadBody(t, resp))
	}
	var out assistantSettingsBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Settings.AssistantEnabled, out.Settings.ApprovedRunsStartFollowUps
}

func (f assistantSettingsFx) get(t *testing.T) (access, followUps bool) {
	t.Helper()
	resp := requestJSON(t, f.c, http.MethodGet, f.url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out assistantSettingsBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Settings.AssistantEnabled, out.Settings.ApprovedRunsStartFollowUps
}

// summaries is the audit summaries of the settings PUTs, oldest first.
func (f assistantSettingsFx) summaries(t *testing.T) []string {
	t.Helper()
	rows, err := f.s.DB.Pool().Query(t.Context(), `SELECT summary FROM audit_events WHERE action = 'PUT updateAssistantSettings' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sum string
		if err := rows.Scan(&sum); err != nil {
			t.Fatal(err)
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAssistantSettingsPartialUpdatesAndTheirAudit: either field alone
// keeps the other, and each PUT's audit summary names what it changed.
func TestAssistantSettingsPartialUpdatesAndTheirAudit(t *testing.T) {
	f := newAssistantSettingsFx(t, "asst-part")
	steps := []struct {
		name                     string
		body                     map[string]any
		wantAccess, wantFollowUp bool
		wantSummary              string
	}{
		{"follow-ups only turned on", map[string]any{"approved_runs_start_follow_ups": true}, false, true, "approved run requests start follow-ups"},
		{"access only", map[string]any{"assistant_enabled": true}, true, true, "turned assistant access on"},
		{"follow-ups only turned off", map[string]any{"approved_runs_start_follow_ups": false}, true, false, "approved run requests start no follow-ups"},
		{"both changed", map[string]any{"assistant_enabled": false, "approved_runs_start_follow_ups": true}, false, true, "turned assistant access off; approved run requests start follow-ups"},
		{"nothing changed", map[string]any{"assistant_enabled": false, "approved_runs_start_follow_ups": true}, false, true, "turned assistant access off"},
		{"nothing changed, follow-ups only", map[string]any{"approved_runs_start_follow_ups": true}, false, true, "approved run requests start follow-ups"},
	}
	for i, st := range steps {
		access, followUps := f.put(t, st.body)
		if access != st.wantAccess || followUps != st.wantFollowUp {
			t.Fatalf("%s: response access %t, follow-ups %t; want %t, %t", st.name, access, followUps, st.wantAccess, st.wantFollowUp)
		}
		if access, followUps = f.get(t); access != st.wantAccess || followUps != st.wantFollowUp {
			t.Fatalf("%s: stored access %t, follow-ups %t; want %t, %t", st.name, access, followUps, st.wantAccess, st.wantFollowUp)
		}
		sums := f.summaries(t)
		if len(sums) != i+1 || sums[i] != st.wantSummary {
			t.Fatalf("%s: audit summaries %q; want the last to be %q", st.name, sums, st.wantSummary)
		}
	}
}

// TestAssistantSettingsRefusesAnEmptyUpdate: a PUT naming neither field
// changes nothing and says so.
func TestAssistantSettingsRefusesAnEmptyUpdate(t *testing.T) {
	f := newAssistantSettingsFx(t, "asst-empty")
	f.put(t, map[string]any{"assistant_enabled": true, "approved_runs_start_follow_ups": true})
	resp := requestJSON(t, f.c, http.MethodPut, f.url, map[string]any{})
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "nothing to change") ||
		!strings.Contains(body, "assistant_enabled") || !strings.Contains(body, "approved_runs_start_follow_ups") {
		t.Fatalf("empty PUT = %d %s, want 422", resp.StatusCode, body)
	}
	if access, followUps := f.get(t); !access || !followUps {
		t.Fatalf("after the empty PUT access %t, follow-ups %t; want both kept on", access, followUps)
	}
	if sums := f.summaries(t); len(sums) != 1 {
		t.Fatalf("audit summaries %q, want only the first PUT's", sums)
	}
}

// TestAssistantSettingsConcurrentPartialPuts: one PUT changing only access
// and one changing only follow-ups, sent at the same time, both land.
func TestAssistantSettingsConcurrentPartialPuts(t *testing.T) {
	f := newAssistantSettingsFx(t, "asst-conc")
	for round := range 5 {
		on := round%2 == 0
		bodies := []map[string]any{{"assistant_enabled": on}, {"approved_runs_start_follow_ups": on}}
		errs := make([]error, len(bodies))
		var wg sync.WaitGroup
		for i, body := range bodies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = putAsync(f.c, f.url, body)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		if access, followUps := f.get(t); access != on || followUps != on {
			t.Fatalf("round %d: access %t, follow-ups %t; want both %t", round, access, followUps, on)
		}
	}
	if sums := f.summaries(t); !slices.Contains(sums, "turned assistant access on") || !slices.Contains(sums, "approved run requests start follow-ups") {
		t.Fatalf("audit summaries %q", sums)
	}
}

// putAsync is a PUT safe to send from a goroutine: it reports instead of
// failing the test.
func putAsync(c *http.Client, url string, body map[string]any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("PUT " + string(b) + " = " + resp.Status)
	}
	return nil
}
