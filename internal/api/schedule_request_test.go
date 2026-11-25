package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// scheduleRequestFixture is a run fixture with an assistant token holding
// automation.request (spec 2026-11-25 assistant API gaps, C).
type scheduleRequestFixture struct {
	runRequestFixture
	auto string
}

func newScheduleRequestFixture(t *testing.T, slug string) scheduleRequestFixture {
	t.Helper()
	f := newRunRequestFixture(t, slug)
	operator := f.memberID(t, slug+"-operator@example.test")
	auto := f.s.AssistantTokenFor(t, f.orgID, operator, tenancy.RoleOperator, tenancy.AreaAutomationRequest)
	return scheduleRequestFixture{runRequestFixture: f, auto: auto}
}

type scheduleRequestDetail struct {
	Request struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		TargetID string `json:"target_id"`
	} `json:"request"`
	Picture struct {
		TemplateName string `json:"template_name"`
		Schedule     *struct {
			Action             string          `json:"action"`
			ScheduleID         string          `json:"schedule_id"`
			New                json.RawMessage `json:"new"`
			Current            json.RawMessage `json:"current"`
			AssistantOverrides bool            `json:"assistant_overrides"`
		} `json:"schedule"`
	} `json:"picture"`
	Fingerprint  string `json:"fingerprint"`
	ApprovalPath string `json:"approval_path"`
}

func (f scheduleRequestFixture) ask(t *testing.T, path string, body map[string]any, want int) scheduleRequestDetail {
	t.Helper()
	resp := f.s.AssistantDo(t, http.MethodPost, f.auto, f.orgPath()+path, "", body)
	if resp.Code != want {
		t.Fatalf("POST %s = %d %s, want %d", path, resp.Code, resp.Body, want)
	}
	var out scheduleRequestDetail
	if want == http.StatusCreated {
		if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (f scheduleRequestFixture) createBody() map[string]any {
	body := f.scheduleBody("Nightly safety net")
	body["reason"] = "re-run the rollout every night"
	return body
}

func (f scheduleRequestFixture) scheduleRow(t *testing.T, id string) (paused, untrusted bool, cron string, createdBy uuid.UUID) {
	t.Helper()
	err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT paused, untrusted_overrides, cron, created_by FROM schedules WHERE id = $1`, id).
		Scan(&paused, &untrusted, &cron, &createdBy)
	if err != nil {
		t.Fatal(err)
	}
	return paused, untrusted, cron, createdBy
}

// An assistant asks for a schedule; nothing exists until a person
// approves it, and then the schedule exists exactly as pictured, under
// the id the picture named, created by the approver.
func TestScheduleRequestCreateAndApprove(t *testing.T) {
	f := newScheduleRequestFixture(t, "sr-create")
	created := f.ask(t, "/schedule-requests", f.createBody(), http.StatusCreated)
	if created.Request.Kind != "schedule" || created.Request.Status != "pending" || created.Request.TargetID == "" ||
		created.Picture.Schedule == nil || created.Picture.Schedule.Action != "create" || created.Picture.Schedule.ScheduleID != created.Request.TargetID ||
		created.ApprovalPath != "/o/sr-create/runs/requests/"+created.Request.ID {
		t.Fatalf("created = %+v", created)
	}
	if !strings.Contains(string(created.Picture.Schedule.New), `"name":"Nightly safety net"`) || created.Picture.TemplateName != "deploy" {
		t.Fatalf("picture = %s", created.Picture.Schedule.New)
	}
	var n int
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT count(*) FROM schedules WHERE org_id = $1`, f.orgID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("schedules before the approval = %d %v", n, err)
	}
	d := f.detail(t, created.Request.ID)
	// A token never approves; a viewer lacks the permission.
	if resp := f.s.AssistantDo(t, http.MethodPost, f.auto, f.orgPath()+"/run-requests/"+created.Request.ID+"/approve", "", map[string]any{"fingerprint": d.Fingerprint}); resp.Code != http.StatusForbidden {
		t.Fatalf("token approve = %d", resp.Code)
	}
	if resp := f.approve(t, f.viewerC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer approve = %d", resp.StatusCode)
	}
	resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var approved struct {
		Request struct {
			Status     string `json:"status"`
			RunGroupID string `json:"run_group_id"`
		} `json:"request"`
	}
	decodeInto(t, resp, &approved)
	if approved.Request.Status != "approved" || approved.Request.RunGroupID != "" {
		t.Fatalf("approved = %+v", approved.Request)
	}
	paused, untrusted, cron, by := f.scheduleRow(t, created.Request.TargetID)
	if paused || untrusted || cron != "0 3 * * 2,4" || by != f.memberID(t, "sr-create-admin@example.test") {
		t.Fatalf("schedule paused=%v untrusted=%v cron=%q by=%s", paused, untrusted, cron, by)
	}
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST requestSchedule", "run_request", created.Request.ID)
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST approveRunRequest", "run_request", created.Request.ID)
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST createSchedule", "schedule", created.Request.TargetID)
}

func TestScheduleRequestRefusals(t *testing.T) {
	f := newScheduleRequestFixture(t, "sr-refuse")
	// Only automation.request asks.
	body := f.createBody()
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/schedule-requests", "", body); resp.Code != http.StatusForbidden {
		t.Fatalf("jobs.request token = %d, want 403", resp.Code)
	}
	body["cron"], body["mode"] = "61 * * * *", "cron"
	delete(body, "simple")
	f.ask(t, "/schedule-requests", body, http.StatusUnprocessableEntity)
	body = f.createBody()
	body["template_id"] = uuid.Must(uuid.NewV7()).String()
	f.ask(t, "/schedule-requests", body, http.StatusNotFound)
	// Extra vars need a schema; never a name Ansible acts on.
	body = f.createBody()
	body["extra_vars"] = map[string]any{"release": "1"}
	f.ask(t, "/schedule-requests", body, http.StatusUnprocessableEntity)
	updateTemplate(t, f.runRequestFixture, func(in *templates.UpdateInput) {
		in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "release", Type: schema.TypeString}, {Name: "deploy_tag", Type: schema.TypeString}}}
	})
	body["extra_vars"] = map[string]any{"ansible_user": "root"}
	f.ask(t, "/schedule-requests", body, http.StatusUnprocessableEntity)
	// A person's schedule name is taken.
	if resp := postJSON(t, f.adminC, f.schedulesURL(), f.scheduleBody("Taken")); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	body = f.createBody()
	body["name"] = "Taken"
	f.ask(t, "/schedule-requests", body, http.StatusUnprocessableEntity)
	// Resuming an active schedule is nothing to ask for.
	var id string
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT id FROM schedules WHERE name = 'Taken'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "/schedules/"+id+"/resume-requests", map[string]any{"reason": "x"}, http.StatusUnprocessableEntity)
	f.ask(t, "/schedules/"+uuid.Must(uuid.NewV7()).String()+"/resume-requests", map[string]any{"reason": "x"}, http.StatusNotFound)
}

// The assistant's extra vars reach the schedule as untrusted values.
func TestScheduleRequestExtraVarsAreUntrusted(t *testing.T) {
	f := newScheduleRequestFixture(t, "sr-untrusted")
	updateTemplate(t, f.runRequestFixture, func(in *templates.UpdateInput) {
		in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "release", Type: schema.TypeString}, {Name: "deploy_tag", Type: schema.TypeString}}}
	})
	body := f.createBody()
	body["extra_vars"] = map[string]any{"release": "{{ lookup('pipe', 'id') }}"}
	created := f.ask(t, "/schedule-requests", body, http.StatusCreated)
	if !created.Picture.Schedule.AssistantOverrides || !strings.Contains(string(created.Picture.Schedule.New), "lookup") {
		t.Fatalf("picture = %+v %s", created.Picture.Schedule, created.Picture.Schedule.New)
	}
	d := f.detail(t, created.Request.ID)
	if resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if _, untrusted, _, _ := f.scheduleRow(t, created.Request.TargetID); !untrusted {
		t.Fatal("the assistant's extra vars are trusted")
	}
}

// An update request shows the schedule as it is and as it will be; the
// approval applies it at the revision the person saw, and a change in
// between asks the person to review again. A resume request resumes.
func TestScheduleRequestUpdateAndResume(t *testing.T) {
	f := newScheduleRequestFixture(t, "sr-update")
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
	id := sched.Schedule.ID
	if resp := postJSON(t, f.adminC, f.schedulesURL()+"/"+id+"/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}

	upd := f.ask(t, "/schedules/"+id+"/update-requests", map[string]any{"reason": "every half hour", "cron": "*/30 * * * *"}, http.StatusCreated)
	ps := upd.Picture.Schedule
	if ps.Action != "update" || !strings.Contains(string(ps.Current), `"cron":"0 3 * * 2,4"`) || !strings.Contains(string(ps.New), `"cron":"*/30 * * * *"`) ||
		!strings.Contains(string(ps.New), `"name":"Nightly"`) {
		t.Fatalf("update picture current %s new %s", ps.Current, ps.New)
	}
	d := f.detail(t, upd.Request.ID)
	// A person renames the schedule meanwhile: the picture changed.
	rename := f.scheduleBody("Renamed")
	if resp := requestWithHeaders(t, f.adminC, http.MethodPut, f.schedulesURL()+"/"+id, rename, map[string]string{"If-Match": `"2"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := f.approve(t, f.adminC, upd.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve after a change = %d, want 409", resp.StatusCode)
	}
	d = f.detail(t, upd.Request.ID)
	if resp := f.approve(t, f.adminC, upd.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	paused, _, cron, _ := f.scheduleRow(t, id)
	if !paused || cron != "*/30 * * * *" {
		t.Fatalf("after the update paused=%v cron=%q", paused, cron)
	}

	res := f.ask(t, "/schedules/"+id+"/resume-requests", map[string]any{"reason": "the fix is out"}, http.StatusCreated)
	if res.Picture.Schedule.Action != "resume" || !strings.Contains(string(res.Picture.Schedule.Current), `"paused":true`) {
		t.Fatalf("resume picture = %+v", res.Picture.Schedule)
	}
	d = f.detail(t, res.Request.ID)
	if resp := f.approve(t, f.adminC, res.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve resume = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if paused, _, _, _ := f.scheduleRow(t, id); paused {
		t.Fatal("the approved resume left the schedule paused")
	}
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST updateSchedule", "schedule", id)
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST resumeSchedule", "schedule", id)
}

// Pausing and deleting only stop runs: automation.request does them
// directly (decision D1), templates.read alone does not.
func TestAutomationTokenPausesAndDeletesDirectly(t *testing.T) {
	f := newScheduleRequestFixture(t, "sr-direct")
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
	path := f.orgPath() + "/schedules/" + sched.Schedule.ID
	operator := f.memberID(t, "sr-direct-operator@example.test")
	reader := f.s.AssistantTokenFor(t, f.orgID, operator, tenancy.RoleOperator, tenancy.AreaTemplatesRead)
	if resp := f.s.AssistantDo(t, http.MethodPost, reader, path+"/pause", "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("templates.read pause = %d", resp.Code)
	}
	if resp := f.s.AssistantDo(t, http.MethodPost, f.auto, path+"/pause", "", nil); resp.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", resp.Code, resp.Body)
	}
	// Resuming is a request, never direct.
	if resp := f.s.AssistantDo(t, http.MethodPost, f.auto, path+"/resume", "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("direct resume = %d", resp.Code)
	}
	if resp := f.s.AssistantDo(t, http.MethodDelete, reader, path, "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("templates.read delete = %d", resp.Code)
	}
	if resp := f.s.AssistantDo(t, http.MethodDelete, f.auto, path, "", nil); resp.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", resp.Code, resp.Body)
	}
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if _, err := f.s.Deps.Schedules.Get(scoped, f.orgID, uuid.MustParse(sched.Schedule.ID)); err == nil {
		t.Fatal("the schedule still exists")
	}
}
