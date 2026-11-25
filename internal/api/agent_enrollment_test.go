package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
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

	if resp := f.s.Put(t, f.apiToken(t), f.templatePath(), "", f.templateBody(map[string]any{"agent_enrollment": true})); resp.Code != http.StatusForbidden {
		t.Fatalf("api token switching it on = %d %s, want 403 (a credential never enrols)", resp.Code, resp.Body)
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

// apiToken is a personal api token of the fixture's operator: the role
// holds agents.write, but a credential never enrols agents (backlog 7.49,
// controller ruling B).
func (f runRequestFixture) apiToken(t *testing.T) string {
	t.Helper()
	operator := f.memberID(t, f.orgSlug+"-operator@example.test")
	_, tok := f.s.PersonalToken(t, operator, map[uuid.UUID]tenancy.Role{f.orgID: tenancy.RoleOperator})
	return tok
}

// enrolOn switches the fixture template's agent enrollment on.
func (f runRequestFixture) enrolOn(t *testing.T) {
	t.Helper()
	put := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"agent_enrollment": true}))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("switch on = %d %s", put.StatusCode, mustReadBody(t, put))
	}
}

// confirmRefusal reads a 409 asking to confirm the enrolled hosts: the
// hosts the run enrols now, in its enroll_hosts.
func confirmRefusal(t *testing.T, resp *http.Response) []string {
	t.Helper()
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", resp.StatusCode, body)
	}
	var problem struct {
		Errors []struct {
			Location string `json:"location"`
		} `json:"errors"`
		EnrollHosts []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"enroll_hosts"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil || len(problem.Errors) != 1 || problem.Errors[0].Location != "body.enroll_host_ids" {
		t.Fatalf("409 body = %s, want one detail at body.enroll_host_ids", body)
	}
	var names []string
	for _, h := range problem.EnrollHosts {
		names = append(names, h.Name+"="+h.ID)
	}
	return names
}

// TestScheduleOfAnEnrollingTemplateNeedsNothingMore (controller ruling A):
// a schedule's runs never mint, so a schedule of a template with agent
// enrollment needs no more than any other.
func TestScheduleOfAnEnrollingTemplateNeedsNothingMore(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-schedule")
	f.enrolOn(t)
	resp := f.s.AssistantDo(t, http.MethodPost, f.enrollmentToken(t), f.orgPath()+"/schedules", "", f.scheduleBody("nightly"))
	if resp.Code != http.StatusCreated {
		t.Fatalf("assistant schedule = %d %s, want 201", resp.Code, resp.Body)
	}
}

// TestManualRunOfAnEnrollingTemplate (controller rulings B, C): a person
// starts a run of a template with agent enrollment only with the hosts
// they confirmed, which must be the targets without an agent now (the
// preview lists them); the job stores them. Without the field, or with
// another list, 409 with the current list; an api token, 403. A dry run
// runs without the field and stores no list; a template without the flag
// refuses a list.
func TestManualRunOfAnEnrollingTemplate(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-run")
	runURL := f.s.HTTP.URL + f.templatePath() + "/run"
	plain := postJSON(t, f.operatorC, runURL, map[string]any{"enroll_host_ids": []string{}})
	if body := mustReadBody(t, plain); plain.StatusCode != http.StatusConflict || !strings.Contains(body, "does not enrol agents") {
		t.Fatalf("a list for a template without the flag = %d %s, want 409", plain.StatusCode, body)
	}
	f.enrolOn(t)

	preview := requestJSON(t, f.operatorC, http.MethodGet, f.s.HTTP.URL+f.templatePath()+"/enrollment-preview", nil)
	var p struct {
		Enrols bool `json:"enrols"`
		Hosts  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"hosts"`
	}
	decodeInto(t, preview, &p)
	if !p.Enrols || len(p.Hosts) != 1 || p.Hosts[0].Name != "web-1" || p.Hosts[0].ID != f.hostA.String() {
		t.Fatalf("preview = %+v, want web-1", p)
	}

	want := []string{"web-1=" + f.hostA.String()}
	if got := confirmRefusal(t, postJSON(t, f.operatorC, runURL, nil)); !slices.Equal(got, want) {
		t.Fatalf("run without confirmation lists %v, want %v", got, want)
	}
	if got := confirmRefusal(t, postJSON(t, f.operatorC, runURL, map[string]any{"enroll_host_ids": []string{f.hostB.String()}})); !slices.Equal(got, want) {
		t.Fatalf("run with another list lists %v, want %v", got, want)
	}
	if resp := f.s.Post(t, f.apiToken(t), f.templatePath()+"/run", map[string]any{"enroll_host_ids": []string{f.hostA.String()}}); resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "agents.write") {
		t.Fatalf("api token = %d %s, want 403", resp.Code, resp.Body)
	}

	started := runJobWith(t, f.operatorC, runURL, map[string]any{"enroll_host_ids": []string{f.hostA.String()}})
	j, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, mustUUID(t, started[0].ID))
	if err != nil || !slices.Equal(j.EnrollHostIDs, []uuid.UUID{f.hostA}) {
		t.Fatalf("started job = %+v, %v; want it enrolling %s", j.EnrollHostIDs, err, f.hostA)
	}

	dry := postJSON(t, f.operatorC, f.s.HTTP.URL+f.orgPath()+"/playbooks/"+f.playbook.String()+"/dry-run",
		map[string]any{"template_id": f.templateID.String()})
	var dryJobs struct {
		Jobs []jobDTO `json:"jobs"`
	}
	if dry.StatusCode != http.StatusAccepted {
		t.Fatalf("dry run = %d %s", dry.StatusCode, mustReadBody(t, dry))
	}
	decodeInto(t, dry, &dryJobs)
	if j, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, mustUUID(t, dryJobs.Jobs[0].ID)); err != nil || j.EnrollHostIDs != nil {
		t.Fatalf("dry run job enrols %v, %v; want no list", j.EnrollHostIDs, err)
	}
}

// TestRetryOfAnEnrollingTemplate (controller rulings B, C, finding 4):
// the retry route asks for the failed hosts without an agent like the run
// route, refuses an api token, and stores the confirmed list.
func TestRetryOfAnEnrollingTemplate(t *testing.T) {
	f := newRunRequestFixture(t, "enrol-retry")
	failed := runJob(t, f.operatorC, f.runURLFor(f.templateID))[0]
	finishJob(t, f.s, f.zoneA, mustUUID(t, failed.ID), jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts: []any{f.hostA.String()}, jobs.StatsUnreachableHosts: []any{},
	}, "playbook failed")
	f.enrolOn(t)
	retryPath := f.orgPath() + "/jobs/" + failed.ID + "/retry-failed"

	preview := requestJSON(t, f.operatorC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/jobs/"+failed.ID+"/enrollment-preview", nil)
	var p struct {
		Enrols bool `json:"enrols"`
		Hosts  []struct {
			Name string `json:"name"`
		} `json:"hosts"`
	}
	decodeInto(t, preview, &p)
	if !p.Enrols || len(p.Hosts) != 1 || p.Hosts[0].Name != "web-1" {
		t.Fatalf("retry preview = %+v, want web-1", p)
	}

	want := []string{"web-1=" + f.hostA.String()}
	if got := confirmRefusal(t, postJSON(t, f.operatorC, f.s.HTTP.URL+retryPath, nil)); !slices.Equal(got, want) {
		t.Fatalf("retry without confirmation lists %v, want %v", got, want)
	}
	if got := confirmRefusal(t, postJSON(t, f.operatorC, f.s.HTTP.URL+retryPath, map[string]any{"enroll_host_ids": []string{}})); !slices.Equal(got, want) {
		t.Fatalf("retry with another list lists %v, want %v", got, want)
	}
	if resp := f.s.Post(t, f.apiToken(t), retryPath, map[string]any{"enroll_host_ids": []string{f.hostA.String()}}); resp.Code != http.StatusForbidden {
		t.Fatalf("api token retry = %d %s, want 403", resp.Code, resp.Body)
	}
	started := runJobWith(t, f.operatorC, f.s.HTTP.URL+retryPath, map[string]any{"enroll_host_ids": []string{f.hostA.String()}})
	j, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, mustUUID(t, started[0].ID))
	if err != nil || !slices.Equal(j.EnrollHostIDs, []uuid.UUID{f.hostA}) {
		t.Fatalf("retry job = %+v, %v; want it enrolling %s", j.EnrollHostIDs, err, f.hostA)
	}
}
