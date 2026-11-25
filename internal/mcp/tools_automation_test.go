package mcp_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

var automationTools = []string{"create_schedule", "update_schedule", "resume_schedule", "pause_schedule", "delete_schedule"}

// automation.request lists the schedule request tools and the direct
// pause and delete; no other area does (spec 2026-11-25 C).
func TestAutomationToolsListedByArea(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest)
	names := toolNames(t, mustConnect(t, f.s, f.token))
	for _, n := range automationTools {
		if !names[n] {
			t.Errorf("%s is not listed for automation.request", n)
		}
	}
	var without []tenancy.Area
	for _, a := range allAreas {
		if a != tenancy.AreaAutomationRequest {
			without = append(without, a)
		}
	}
	other := newOrgFixture(t, without...)
	names = toolNames(t, mustConnect(t, other.s, other.token))
	for _, n := range automationTools {
		if names[n] {
			t.Errorf("%s is listed without automation.request", n)
		}
	}
}

type requestResult struct {
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	ApprovalURL string `json:"approval_url"`
	Note        string `json:"note"`
	Picture     struct {
		Schedule struct {
			Action     string         `json:"action"`
			ScheduleID string         `json:"schedule_id"`
			New        map[string]any `json:"new"`
		} `json:"schedule"`
	} `json:"picture"`
}

func (f orgFixture) approveSchedule(t *testing.T, id string) runrequests.Approved {
	t.Helper()
	rid := uuid.MustParse(id)
	_, _, fp, err := f.s.Deps.RunRequests.Get(f.scoped, f.org, rid)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.s.Deps.RunRequests.ApproveWith(f.scoped, f.org, rid, f.owner, fp, nil, runrequests.Approver{WriteTemplates: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCreateScheduleToolAsksForApproval(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest)
	cs := mustConnect(t, f.s, f.token)
	var req requestResult
	callOK(t, cs, "create_schedule", map[string]any{
		"template_id": f.template.String(), "name": "nightly", "mode": "simple",
		"simple":   map[string]any{"kind": "daily", "time": "03:00"},
		"timezone": "UTC", "guards": map[string]any{"overlap": "queue"}, "reason": "safety net",
	}, &req)
	if req.Status != "pending" || !strings.HasSuffix(req.ApprovalURL, "/runs/requests/"+req.RequestID) || req.Picture.Schedule.Action != "create" ||
		req.Picture.Schedule.New["name"] != "nightly" || !strings.Contains(req.Note, "schedule") {
		t.Fatalf("create_schedule = %+v", req)
	}
	if list, _, _ := f.s.Deps.Schedules.List(f.scoped, f.org, nil); len(list) != 0 {
		t.Fatalf("a schedule exists before the approval: %+v", list)
	}
	f.approveSchedule(t, req.RequestID)
	s, err := f.s.Deps.Schedules.Get(f.scoped, f.org, uuid.MustParse(req.Picture.Schedule.ScheduleID))
	if err != nil || s.Name != "nightly" || s.Guards.Overlap != schedules.OverlapQueue || s.Cron != "0 3 * * *" {
		t.Fatalf("schedule = %+v %v", s, err)
	}
}

func TestUpdateResumePauseDeleteScheduleTools(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest)
	id := f.seedSchedule(t)
	cs := mustConnect(t, f.s, f.token)

	callOK(t, cs, "pause_schedule", map[string]any{"schedule_id": id}, nil)
	s, err := f.s.Deps.Schedules.Get(f.scoped, f.org, uuid.MustParse(id))
	if err != nil || !s.Paused {
		t.Fatalf("after pause_schedule = %+v %v", s, err)
	}

	var upd requestResult
	callOK(t, cs, "update_schedule", map[string]any{"schedule_id": id, "cron": "*/15 * * * *", "reason": "more often"}, &upd)
	if upd.Picture.Schedule.Action != "update" || upd.Picture.Schedule.New["cron"] != "*/15 * * * *" || upd.Picture.Schedule.New["name"] != "nightly" {
		t.Fatalf("update_schedule = %+v", upd)
	}
	f.approveSchedule(t, upd.RequestID)
	if s, _ = f.s.Deps.Schedules.Get(f.scoped, f.org, uuid.MustParse(id)); s.Cron != "*/15 * * * *" || !s.Paused {
		t.Fatalf("after the approved update = %+v", s)
	}

	var res requestResult
	callOK(t, cs, "resume_schedule", map[string]any{"schedule_id": id, "reason": "fixed"}, &res)
	if res.Picture.Schedule.Action != "resume" {
		t.Fatalf("resume_schedule = %+v", res)
	}
	f.approveSchedule(t, res.RequestID)
	if s, _ = f.s.Deps.Schedules.Get(f.scoped, f.org, uuid.MustParse(id)); s.Paused {
		t.Fatal("the approved resume left the schedule paused")
	}

	callOK(t, cs, "delete_schedule", map[string]any{"schedule_id": id}, nil)
	if _, err := f.s.Deps.Schedules.Get(f.scoped, f.org, uuid.MustParse(id)); err == nil {
		t.Fatal("the schedule survived delete_schedule")
	}
}

// Extra vars keep their numbers exact, and a refusal reaches the
// assistant as a tool error.
func TestScheduleToolsExtraVars(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaAutomationRequest)
	cur, err := f.s.Deps.Templates.Get(f.scoped, f.org, f.template)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Templates.Update(f.scoped, f.org, f.template, templates.UpdateInput{
		Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope,
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "build", Type: schema.TypeFloat}}},
	}); err != nil {
		t.Fatal(err)
	}
	cs := mustConnect(t, f.s, f.token)
	var req struct {
		Picture json.RawMessage `json:"picture"`
	}
	callOK(t, cs, "create_schedule", map[string]any{
		"template_id": f.template.String(), "name": "exact", "mode": "cron", "cron": "0 3 * * *", "timezone": "UTC",
		"extra_vars": json.RawMessage(`{"build":9007199254740993}`), "reason": "x",
	}, &req)
	if !regexp.MustCompile(`"build":\s*9007199254740993\b`).Match(req.Picture) {
		t.Fatalf("picture lost digits: %s", req.Picture)
	}
	text, isErr := call(t, cs, "create_schedule", map[string]any{
		"template_id": f.template.String(), "name": "bad", "mode": "cron", "cron": "0 3 * * *", "timezone": "UTC",
		"extra_vars": map[string]any{"ansible_user": "root"}, "reason": "x",
	})
	if !isErr || !strings.Contains(text, "ansible_user") {
		t.Fatalf("an ansible name = %v %s", isErr, text)
	}
}
