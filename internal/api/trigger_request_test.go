package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
)

type triggerRequestDetail struct {
	Request struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		TargetID   string `json:"target_id"`
		TemplateID string `json:"template_id"`
	} `json:"request"`
	Picture struct {
		TemplateName string `json:"template_name"`
		Trigger      *struct {
			Action    string          `json:"action"`
			TriggerID string          `json:"trigger_id"`
			URL       string          `json:"url"`
			New       json.RawMessage `json:"new"`
			Current   json.RawMessage `json:"current"`
			Gone      bool            `json:"gone"`
		} `json:"trigger"`
	} `json:"picture"`
	Fingerprint string `json:"fingerprint"`
}

type triggerApproval struct {
	Request struct {
		Status   string `json:"status"`
		TargetID string `json:"target_id"`
	} `json:"request"`
	TriggerSecret string `json:"trigger_secret"`
	TriggerURL    string `json:"trigger_url"`
}

func (f scheduleRequestFixture) askTrigger(t *testing.T, path string, body map[string]any, want int) (triggerRequestDetail, string) {
	t.Helper()
	resp := f.s.AssistantDo(t, http.MethodPost, f.auto, f.orgPath()+path, "", body)
	if resp.Code != want {
		t.Fatalf("POST %s = %d %s, want %d", path, resp.Code, resp.Body, want)
	}
	var out triggerRequestDetail
	if want == http.StatusCreated {
		if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, resp.Body.String()
}

// approveTrigger approves request id as the admin, with the fingerprint
// of its current picture, and wants want.
func (f scheduleRequestFixture) approveTrigger(t *testing.T, id string, want int) (triggerApproval, *http.Response, string) {
	t.Helper()
	d := f.detail(t, id)
	resp := f.approve(t, f.adminC, id, d.Fingerprint)
	body := mustReadBody(t, resp)
	if resp.StatusCode != want {
		t.Fatalf("approve %s = %d %s, want %d", id, resp.StatusCode, body, want)
	}
	var out triggerApproval
	if want == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, resp, body
}

func (f scheduleRequestFixture) storedTrigger(t *testing.T, id string) triggers.Trigger {
	t.Helper()
	tr, err := f.s.Deps.Triggers.Get(db.WithOrgScope(context.Background(), f.orgID), f.orgID, uuid.MustParse(id))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func (f scheduleRequestFixture) triggerBody() map[string]any {
	return map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on tag", "preset": "pulsix",
		"allow_cidrs": []string{"192.0.2.0/24", "127.0.0.0/8"}, "rate_limit": map[string]any{"count": 3, "per": "1m"},
		"mapping": map[string]any{"extra_vars": map[string]string{"deploy_tag": "$.ref"}},
		"reason":  "deploy every release tag",
	}
}

// secretsIn counts the places a value is found in the database's activity
// and audit trail, and in the stored requests.
func (f scheduleRequestFixture) secretsIn(t *testing.T, secret string) int {
	t.Helper()
	var n int
	err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT
	  (SELECT count(*) FROM audit_events WHERE strpos(audit_events::text, $1) > 0) +
	  (SELECT count(*) FROM run_requests WHERE strpos(run_requests::text, $1) > 0)`, secret).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// An assistant asks for a webhook trigger; nothing exists until a person
// approves it. The approval creates the trigger exactly as pictured, under
// the id (and so the URL) the picture named, and its response alone carries
// the secret, uncached: the request, the detail, the activity and the audit
// trail never hold it, and the assistant's own view of the request has none.
func TestTriggerRequestCreateAndApprove(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-create")
	created, raw := f.askTrigger(t, "/trigger-requests", f.triggerBody(), http.StatusCreated)
	pt := created.Picture.Trigger
	if created.Request.Kind != "trigger" || created.Request.Status != "pending" || pt == nil || pt.Action != "create" ||
		pt.TriggerID != created.Request.TargetID || !strings.HasSuffix(pt.URL, "/api/v1/hooks/jobs/"+created.Request.TargetID) ||
		created.Picture.TemplateName != "deploy" {
		t.Fatalf("created = %+v / %+v", created, pt)
	}
	for _, want := range []string{`"name":"deploy on tag"`, `"allow_cidrs":["192.0.2.0/24","127.0.0.0/8"]`, `"count":3`, `"on_active":"queue"`, `"deploy_tag":"$.ref"`, `"signature_header":"X-Pulsix-Signature-256"`} {
		if !strings.Contains(string(pt.New), want) {
			t.Fatalf("picture lacks %s: %s", want, pt.New)
		}
	}
	if strings.Contains(raw, "secret") {
		t.Fatalf("the request's response mentions a secret: %s", raw)
	}
	if _, err := f.s.Deps.Triggers.Get(f.scoped, f.orgID, uuid.MustParse(created.Request.TargetID)); err == nil {
		t.Fatal("the trigger exists before the approval")
	}

	a, resp, _ := f.approveTrigger(t, created.Request.ID, http.StatusOK)
	if a.Request.Status != "approved" || len(a.TriggerSecret) != 43 || a.TriggerURL != pt.URL {
		t.Fatalf("approval = %+v", a)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	tr := f.storedTrigger(t, created.Request.TargetID)
	if tr.Name != "deploy on tag" || !tr.Enabled || tr.RateLimit.Count != 3 || len(tr.AllowCIDRs) != 2 || tr.CreatedBy == nil || *tr.CreatedBy != f.memberID(t, f.orgSlug+"-admin@example.test") {
		t.Fatalf("trigger = %+v", tr)
	}
	// The secret works: a sender signing with it reaches the trigger.
	body := []byte(`{"ref":"v2"}`)
	c := triggers.Created{Trigger: tr, Secret: a.TriggerSecret}
	if got := hook(t, a.TriggerURL, body, signedHeaders(c, body)); got.StatusCode == http.StatusNotFound || got.StatusCode == http.StatusUnauthorized {
		t.Fatalf("hook with the approval's secret = %d %s", got.StatusCode, mustReadBody(t, got))
	}
	if n := f.secretsIn(t, a.TriggerSecret); n != 0 {
		t.Fatalf("the secret is stored in %d audit or request rows", n)
	}
	detail := f.s.AssistantDo(t, http.MethodGet, f.auto, f.orgPath()+"/run-requests/"+created.Request.ID, "", nil)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), a.TriggerSecret) || strings.Contains(detail.Body.String(), "trigger_secret") {
		t.Fatalf("the assistant's view = %d %s", detail.Code, detail.Body)
	}
	activity := mustReadBody(t, requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/activity?limit=100", nil))
	if !strings.Contains(activity, "created webhook trigger deploy on tag (approved request "+created.Request.ID+")") {
		t.Fatalf("activity lacks the created trigger: %s", activity)
	}
}

// A repository sync trigger's request names no template; approving it
// needs repos.write, not a template.
func TestTriggerRequestOfARepositorySync(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-repo")
	repo := f.createRepo(t, "infra")
	created, _ := f.askTrigger(t, "/trigger-requests", map[string]any{
		"kind": "repo_sync", "repo_id": repo, "name": "push", "preset": "github", "on_active": "skip", "reason": "sync on push",
	}, http.StatusCreated)
	if created.Request.TemplateID != "" || created.Picture.TemplateName != "" || !strings.Contains(string(created.Picture.Trigger.New), `"repo_name":"infra"`) {
		t.Fatalf("created = %+v %s", created, created.Picture.Trigger.New)
	}
	var tmpl *uuid.UUID
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT template_id FROM run_requests WHERE id = $1`, created.Request.ID).Scan(&tmpl); err != nil || tmpl != nil {
		t.Fatalf("template_id = %v %v", tmpl, err)
	}
	a, _, _ := f.approveTrigger(t, created.Request.ID, http.StatusOK)
	if a.TriggerSecret == "" || !strings.Contains(a.TriggerURL, "/api/v1/hooks/repos/") {
		t.Fatalf("approval = %+v", a)
	}
	if tr := f.storedTrigger(t, created.Request.TargetID); tr.Kind != triggers.KindRepoSync || tr.OnActive != triggers.OnActiveSkip {
		t.Fatalf("trigger = %+v", tr)
	}
}

// An update request shows the trigger as it is next to how it will be; the
// approval writes exactly that and carries no secret. A rotation request
// hands the approving person a new secret and leaves a trigger a person
// disabled disabled.
func TestTriggerRequestUpdateAndRotate(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-update")
	c := f.createTrigger(t, func(in *triggers.Input) { in.Name = "hook" })
	id := c.Trigger.ID.String()
	upd, _ := f.askTrigger(t, "/triggers/"+id+"/update-requests", map[string]any{
		"name": "release hook", "allow_cidrs": []string{"198.51.100.0/24"}, "reason": "only from CI",
	}, http.StatusCreated)
	pt := upd.Picture.Trigger
	if pt.Action != "update" || !strings.Contains(string(pt.Current), `"name":"hook"`) || !strings.Contains(string(pt.New), `"name":"release hook"`) ||
		!strings.Contains(string(pt.New), `"198.51.100.0/24"`) || !strings.Contains(string(pt.Current), `"revision":1`) {
		t.Fatalf("picture = %+v", pt)
	}
	a, resp, body := f.approveTrigger(t, upd.Request.ID, http.StatusOK)
	if a.TriggerSecret != "" || strings.Contains(body, "trigger_secret") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("update approval = %s", body)
	}
	tr := f.storedTrigger(t, id)
	if tr.Name != "release hook" || len(tr.AllowCIDRs) != 1 || tr.AllowCIDRs[0].String() != "198.51.100.0/24" || !tr.Enabled {
		t.Fatalf("trigger = %+v", tr)
	}

	rot, _ := f.askTrigger(t, "/triggers/"+id+"/rotate-requests", map[string]any{"reason": "the old secret leaked"}, http.StatusCreated)
	if rot.Picture.Trigger.Action != "rotate" || rot.Picture.Trigger.New != nil {
		t.Fatalf("rotate picture = %+v", rot.Picture.Trigger)
	}
	// A person disables the trigger before anyone approves: the picture
	// changed, so the approval asks for a fresh look first.
	if _, err := f.s.Deps.Triggers.Disable(f.scoped, f.orgID, c.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if resp := f.approve(t, f.adminC, rot.Request.ID, rot.Fingerprint); resp.StatusCode != http.StatusConflict {
		t.Fatalf("approval of a changed picture = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	r, _, _ := f.approveTrigger(t, rot.Request.ID, http.StatusOK)
	if len(r.TriggerSecret) != 43 || r.TriggerSecret == c.Secret {
		t.Fatalf("rotation = %+v", r)
	}
	if tr := f.storedTrigger(t, id); tr.Enabled {
		t.Fatal("the rotation enabled a trigger a person disabled")
	}
	old := []byte(`{"ref":"v3"}`)
	if got := hook(t, c.URL, old, signedHeaders(c, old)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("the old secret still reaches the trigger: %d", got.StatusCode)
	}
}

// A trigger request approved after its template changed shows the new
// template revision first.
func TestTriggerRequestAfterTheTemplateChanged(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-tmpl")
	created, _ := f.askTrigger(t, "/trigger-requests", f.triggerBody(), http.StatusCreated)
	updateTemplate(t, f.runRequestFixture, func(in *templates.UpdateInput) { in.Limit = "web-1" })
	resp := f.approve(t, f.adminC, created.Request.ID, created.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "review it again") {
		t.Fatalf("approval after a template change = %d %s", resp.StatusCode, body)
	}
	f.approveTrigger(t, created.Request.ID, http.StatusOK)
}

// What an assistant may not set is refused when it asks: a mapping onto a
// name Ansible acts on, a name another trigger has, a request without
// automation.request.
func TestTriggerRequestRefusals(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-refuse")
	bad := f.triggerBody()
	bad["mapping"] = map[string]any{"extra_vars": map[string]string{"ansible_become": "$.x"}}
	f.askTrigger(t, "/trigger-requests", bad, http.StatusUnprocessableEntity)
	f.createTrigger(t, func(in *triggers.Input) { in.Name = "deploy on tag" })
	f.askTrigger(t, "/trigger-requests", f.triggerBody(), http.StatusUnprocessableEntity)
	other := f.triggerBody()
	other["name"] = "other"
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/trigger-requests", "", other); resp.Code != http.StatusForbidden {
		t.Fatalf("without automation.request = %d %s", resp.Code, resp.Body)
	}
}

// Disabling and deleting a trigger only stop runs: automation.request
// does both directly. Changing a trigger any other way through the trigger
// routes stays with templates.write.
func TestAutomationRequestDisablesAndDeletesTriggers(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-direct")
	c := f.createTrigger(t, nil)
	path := f.orgPath() + "/triggers/" + c.Trigger.ID.String()
	resp := f.s.AssistantDo(t, http.MethodPost, f.auto, path+"/disable", "", nil)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"enabled":false`) || strings.Contains(resp.Body.String(), c.Secret) {
		t.Fatalf("disable = %d %s", resp.Code, resp.Body)
	}
	if f.storedTrigger(t, c.Trigger.ID.String()).Enabled {
		t.Fatal("the trigger is still enabled")
	}
	// Only the request routes change a trigger for automation.request:
	// enabling it, rotating its secret or a test call directly are refused,
	// with templates.read beside it too.
	both := f.s.AssistantTokenFor(t, f.orgID, f.memberID(t, f.orgSlug+"-operator@example.test"), tenancy.RoleOperator,
		tenancy.AreaAutomationRequest, tenancy.AreaTemplatesRead)
	for _, tok := range []string{f.auto, both} {
		for _, call := range []struct {
			method, path string
			body         any
			ifMatch      string
		}{
			{http.MethodPut, path, map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": c.Trigger.Name, "preset": "pulsix", "enabled": true}, `"2"`},
			{http.MethodPost, path + "/rotate-secret", nil, ""},
			{http.MethodPost, path + "/test", map[string]any{}, ""},
		} {
			if resp := f.s.AssistantDo(t, call.method, tok, call.path, call.ifMatch, call.body); resp.Code != http.StatusForbidden {
				t.Fatalf("%s %s with automation.request = %d %s", call.method, call.path, resp.Code, resp.Body)
			}
		}
	}
	if f.storedTrigger(t, c.Trigger.ID.String()).Enabled {
		t.Fatal("a refused call enabled the trigger")
	}
	if resp := f.s.AssistantDo(t, http.MethodDelete, f.auto, path, "", nil); resp.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", resp.Code, resp.Body)
	}
	if _, err := f.s.Deps.Triggers.Get(f.scoped, f.orgID, c.Trigger.ID); err == nil {
		t.Fatal("the trigger still exists")
	}
	// A token without automation.request (and without templates.write)
	// cannot disable one.
	c2 := f.createTrigger(t, nil)
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/triggers/"+c2.Trigger.ID.String()+"/disable", "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("disable without automation.request = %d %s", resp.Code, resp.Body)
	}
	// A person disables one too.
	if resp := postJSON(t, f.operatorC, f.s.HTTP.URL+f.orgPath()+"/triggers/"+c2.Trigger.ID.String()+"/disable", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a person's disable = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// A viewer cannot.
	if resp := postJSON(t, f.viewerC, f.s.HTTP.URL+f.orgPath()+"/triggers/"+c2.Trigger.ID.String()+"/disable", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer's disable = %d", resp.StatusCode)
	}
}

// An update request that changes nothing, of a trigger or a schedule, is
// refused: there is nothing for a person to approve.
func TestUpdateRequestThatChangesNothing(t *testing.T) {
	f := newScheduleRequestFixture(t, "tr-noop")
	c := f.createTrigger(t, func(in *triggers.Input) { in.Name = "hook" })
	for name, body := range map[string]map[string]any{
		"no field":       {"reason": "nothing"},
		"the same value": {"name": "hook", "reason": "same"},
	} {
		_, raw := f.askTrigger(t, "/triggers/"+c.Trigger.ID.String()+"/update-requests", body, http.StatusUnprocessableEntity)
		if !strings.Contains(raw, "nothing to change") {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	resp := postJSON(t, f.adminC, f.schedulesURL(), f.scheduleBody("Nightly"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var sched struct {
		Schedule struct {
			ID string `json:"id"`
		} `json:"schedule"`
	}
	decodeInto(t, resp, &sched)
	got := f.s.AssistantDo(t, http.MethodPost, f.auto, f.orgPath()+"/schedules/"+sched.Schedule.ID+"/update-requests", "", map[string]any{"name": "Nightly", "reason": "same"})
	if got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), "nothing to change") {
		t.Fatalf("schedule no-op = %d %s", got.Code, got.Body)
	}
}
