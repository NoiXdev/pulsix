package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// enrollmentToken is an assistant token of the fixture's operator with the
// templates.write area: it may change templates and create schedules, but
// no area grants agents.write.
func (f runRequestFixture) enrollmentToken(t *testing.T) string {
	t.Helper()
	operator := f.memberID(t, f.orgSlug+"-operator@example.test")
	return f.s.AssistantTokenFor(t, f.orgID, operator, tenancy.RoleOperator, tenancy.AreaTemplatesWrite, tenancy.AreaJobsRequest)
}

func (f runRequestFixture) templatePath() string {
	return f.orgPath() + "/job-templates/" + f.templateID.String()
}

// templateEnrollment reads the template's agent_enrollment as the API
// serves it.
func (f runRequestFixture) templateEnrollment(t *testing.T) bool {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.templatePath(), nil)
	var out struct {
		Template struct {
			AgentEnrollment *bool `json:"agent_enrollment"`
		} `json:"template"`
	}
	decodeInto(t, resp, &out)
	if out.Template.AgentEnrollment == nil {
		t.Fatal("the template DTO has no agent_enrollment")
	}
	return *out.Template.AgentEnrollment
}

// templateBody is a PUT body for the fixture's template, with fields set.
func (f runRequestFixture) templateBody(fields map[string]any) map[string]any {
	body := map[string]any{
		"name": "deploy", "playbook_id": f.playbook.String(),
		"scope":      map[string]any{"host_ids": []string{f.hostA.String()}},
		"extra_vars": map[string]any{"deploy_tag": "extra-var-s3cret-31d7"},
	}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

// TestTemplateAgentEnrollmentNeedsTheAgentsPermission (spec 2026-11-25 B,
// "Template flag"): a person switches agent enrollment on through the
// template API; an update that leaves it out keeps it. An assistant token,
// whose areas never grant agents.write, cannot switch it either way, but
// can save the template with it as it is (as update_job_template does).
func TestTemplateAgentEnrollmentNeedsTheAgentsPermission(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-flag")
	tok := f.enrollmentToken(t)
	if f.templateEnrollment(t) {
		t.Fatal("a new template enrols agents")
	}

	resp := f.s.AssistantDo(t, http.MethodPut, tok, f.templatePath(), "", f.templateBody(map[string]any{"agent_enrollment": true}))
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "agents.write") {
		t.Fatalf("assistant switching it on = %d %s, want 403 naming agents.write", resp.Code, resp.Body)
	}
	resp = f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/job-templates", "", f.templateBody(map[string]any{"name": "enrol-new", "agent_enrollment": true}))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("assistant creating one with it = %d %s, want 403", resp.Code, resp.Body)
	}

	put := requestJSON(t, f.operatorC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"agent_enrollment": true}))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("operator switching it on = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	if !f.templateEnrollment(t) {
		t.Fatal("the switch did not stick")
	}
	put = requestJSON(t, f.operatorC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"limit": "web*"}))
	if put.StatusCode != http.StatusOK || !f.templateEnrollment(t) {
		t.Fatalf("an update without the key = %d, enrollment %v; want it kept on", put.StatusCode, f.templateEnrollment(t))
	}

	resp = f.s.AssistantDo(t, http.MethodPut, tok, f.templatePath(), "", f.templateBody(map[string]any{"agent_enrollment": true, "limit": "web-1"}))
	if resp.Code != http.StatusOK {
		t.Fatalf("assistant saving with the flag as it is = %d %s, want 200", resp.Code, resp.Body)
	}
	resp = f.s.AssistantDo(t, http.MethodPut, tok, f.templatePath(), "", f.templateBody(map[string]any{"agent_enrollment": false}))
	if resp.Code != http.StatusForbidden || !f.templateEnrollment(t) {
		t.Fatalf("assistant switching it off = %d %s, want 403 and the flag kept", resp.Code, resp.Body)
	}
}

// enrollmentOf is the picture's agent_enrollment as the API serves it.
type pictureEnrollment struct {
	Hosts      []string `json:"hosts"`
	HostIDs    []string `json:"host_ids"`
	NoneReason string   `json:"none_reason"`
}

func enrollmentOf(t *testing.T, d runRequestDetail) *pictureEnrollment {
	t.Helper()
	var p struct {
		AgentEnrollment *pictureEnrollment `json:"agent_enrollment"`
	}
	if err := json.Unmarshal(d.Picture, &p); err != nil {
		t.Fatal(err)
	}
	return p.AgentEnrollment
}

// TestRunRequestOfAnEnrollingTemplate (spec 2026-11-25 B, "Picture"): the
// picture of a request for a template with agent enrollment lists the
// targets without an agent (a dry run's none, saying why); the approval
// stores the list on the request's job.
func TestRunRequestOfAnEnrollingTemplate(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-request")
	if e := enrollmentOf(t, f.request(t, map[string]any{"reason": "plain"})); e != nil {
		t.Fatalf("a template without the flag shows %+v", e)
	}
	put := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"agent_enrollment": true}))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("switch on = %d %s", put.StatusCode, mustReadBody(t, put))
	}

	dry := f.request(t, map[string]any{"reason": "try", "check_mode": true})
	if e := enrollmentOf(t, dry); e == nil || e.NoneReason != "dry_run" || len(e.Hosts) != 0 {
		t.Fatalf("dry run enrollment = %+v, want none, dry_run", e)
	}

	created := f.request(t, map[string]any{"reason": "install the agent"})
	e := enrollmentOf(t, created)
	if e == nil || !slices.Equal(e.Hosts, []string{"web-1"}) || !slices.Equal(e.HostIDs, []string{f.hostA.String()}) {
		t.Fatalf("enrollment = %+v, want web-1", e)
	}
	resp := f.approve(t, f.adminC, created.Request.ID, created.Fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	jobs := f.groupJobs(t, created.Request.ID)
	if len(jobs) != 1 || !slices.Equal(jobs[0].EnrollHostIDs, []uuid.UUID{f.hostA}) {
		t.Fatalf("approved jobs = %+v, want one enrolling %s", jobs, f.hostA)
	}
	if summary := f.approvalSummary(t, created.Request.ID); !strings.Contains(summary, "enrols agents on 1 host") {
		t.Fatalf("approval summary = %q, want the enrollment named", summary)
	}
}

// TestScheduleOfAnEnrollingTemplateNeedsTheAgentsPermission (spec
// 2026-11-25 B, "Who may"): creating or resuming a schedule of a template
// with agent enrollment needs agents.write as well; a person has it, an
// assistant token does not.
func TestScheduleOfAnEnrollingTemplateNeedsTheAgentsPermission(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-schedule")
	tok := f.enrollmentToken(t)
	put := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"agent_enrollment": true}))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("switch on = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	resp := f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/schedules", "", f.scheduleBody("nightly enrol"))
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "agents.write") {
		t.Fatalf("assistant schedule = %d %s, want 403 naming agents.write", resp.Code, resp.Body)
	}

	created := postJSON(t, f.operatorC, f.schedulesURL(), f.scheduleBody("nightly enrol"))
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("operator schedule = %d %s", created.StatusCode, mustReadBody(t, created))
	}
	var s struct {
		Schedule struct {
			ID string `json:"id"`
		} `json:"schedule"`
	}
	decodeInto(t, created, &s)
	if r := postJSON(t, f.operatorC, f.schedulesURL()+"/"+s.Schedule.ID+"/pause", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("pause = %d %s", r.StatusCode, mustReadBody(t, r))
	}
	resp = f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/schedules/"+s.Schedule.ID+"/resume", "", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("assistant resume = %d %s, want 403", resp.Code, resp.Body)
	}
	if r := postJSON(t, f.operatorC, f.schedulesURL()+"/"+s.Schedule.ID+"/resume", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("operator resume = %d %s", r.StatusCode, mustReadBody(t, r))
	}
	// Pausing only stops runs: the assistant may.
	if resp = f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/schedules/"+s.Schedule.ID+"/pause", "", nil); resp.Code != http.StatusOK {
		t.Fatalf("assistant pause = %d %s, want 200", resp.Code, resp.Body)
	}
}

// TestManualRunsOfAnEnrollingTemplate: a person
// with agents.write starts, retries and dry-runs a template with agent
// enrollment as before (the refusal is mayEnrolAgents', unit-tested: no
// principal holds jobs.run without agents.write today).
func TestManualRunsOfAnEnrollingTemplate(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-run")
	put := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"agent_enrollment": true}))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("switch on = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	run := postJSON(t, f.operatorC, f.s.HTTP.URL+f.templatePath()+"/run", nil)
	if run.StatusCode != http.StatusAccepted {
		t.Fatalf("run = %d %s", run.StatusCode, mustReadBody(t, run))
	}
	dry := postJSON(t, f.operatorC, f.s.HTTP.URL+f.orgPath()+"/playbooks/"+f.playbook.String()+"/dry-run",
		map[string]any{"template_id": f.templateID.String()})
	if dry.StatusCode != http.StatusAccepted {
		t.Fatalf("dry run = %d %s", dry.StatusCode, mustReadBody(t, dry))
	}
}
