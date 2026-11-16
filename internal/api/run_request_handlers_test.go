package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type runRequestFixture struct {
	runFixture
	tok    string
	scoped context.Context
}

func newRunRequestFixture(t *testing.T, slug string, opts ...apitest.Option) runRequestFixture {
	t.Helper()
	f := newRunFixtureWith(t, slug, opts...)
	operator := f.memberID(t, slug+"-operator@example.test")
	tok := f.s.AssistantTokenFor(t, f.orgID, operator, tenancy.RoleOperator,
		tenancy.AreaJobsRequest, tenancy.AreaTemplatesRead)
	return runRequestFixture{runFixture: f, tok: tok, scoped: db.WithOrgScope(context.Background(), f.orgID)}
}

func (f runRequestFixture) orgPath() string { return "/api/v1/orgs/" + f.orgSlug }

type runRequestDTO struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	RunGroupID       string `json:"run_group_id"`
	RequestedByToken string `json:"requested_by_token_name"`
}

type runRequestDetail struct {
	Request      runRequestDTO   `json:"request"`
	Picture      json.RawMessage `json:"picture"`
	Fingerprint  string          `json:"fingerprint"`
	ApprovalPath string          `json:"approval_path"`
}

func (f runRequestFixture) request(t *testing.T, body map[string]any) runRequestDetail {
	t.Helper()
	resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/job-templates/"+f.templateID.String()+"/run-requests", "", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create run request = %d %s", resp.Code, resp.Body)
	}
	var out runRequestDetail
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f runRequestFixture) detail(t *testing.T, id string) runRequestDetail {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out runRequestDetail
	decodeInto(t, resp, &out)
	return out
}

func (f runRequestFixture) approve(t *testing.T, c *http.Client, id, fingerprint string) *http.Response {
	t.Helper()
	return postJSON(t, c, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id+"/approve", map[string]any{"fingerprint": fingerprint})
}

func TestRunRequestCreateListDetailAndApprove(t *testing.T) {
	f := newRunRequestFixture(t, "rr-flow")
	created := f.request(t, map[string]any{"reason": "deploy the web tier"})
	if created.Request.Status != "pending" || created.Request.Kind != "run" || created.ApprovalPath != "/o/rr-flow/runs/requests/"+created.Request.ID {
		t.Fatalf("created = %+v", created)
	}

	resp := f.s.AssistantDo(t, http.MethodGet, f.tok, f.orgPath()+"/run-requests?status=pending", "", nil)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), created.Request.ID) {
		t.Fatalf("list = %d %s", resp.Code, resp.Body)
	}

	d := f.detail(t, created.Request.ID)
	if d.Fingerprint == "" || !strings.Contains(string(d.Picture), `"template_name":"deploy"`) || !strings.Contains(string(d.Picture), `"name":"web-1"`) {
		t.Fatalf("detail picture = %s", d.Picture)
	}

	// A token can never approve, whatever its role.
	tokenApprove := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/run-requests/"+created.Request.ID+"/approve", "", map[string]any{"fingerprint": d.Fingerprint})
	if tokenApprove.Code != http.StatusForbidden {
		t.Fatalf("token approve = %d %s, want 403", tokenApprove.Code, tokenApprove.Body)
	}
	// A viewer lacks jobs.run.
	if resp := f.approve(t, f.viewerC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer approve = %d, want 403", resp.StatusCode)
	}

	resp2 := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp2.StatusCode, mustReadBody(t, resp2))
	}
	var approved struct {
		Request runRequestDTO `json:"request"`
	}
	decodeInto(t, resp2, &approved)
	if approved.Request.Status != "approved" || approved.Request.RunGroupID == "" {
		t.Fatalf("approved = %+v", approved.Request)
	}
	jobsResp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+approved.Request.RunGroupID, nil)
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, jobsResp, &list)
	if len(list.Jobs) != 1 || list.Jobs[0].Trigger != "manual" {
		t.Fatalf("enqueued jobs = %+v", list.Jobs)
	}
	// The approver, not the assistant's user, is the run's actor.
	var actor uuid.UUID
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT actor_id FROM jobs WHERE id = $1`, list.Jobs[0].ID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != f.memberID(t, "rr-flow-admin@example.test") {
		t.Fatalf("run actor = %s, want the approving admin", actor)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409", resp.StatusCode)
	}
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST approveRunRequest", "run_request", created.Request.ID)
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST requestRun", "run_request", created.Request.ID)
}

func TestRunRequestValidation(t *testing.T) {
	f := newRunRequestFixture(t, "rr-valid")
	base := f.orgPath() + "/job-templates/"
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, base+uuid.Must(uuid.NewV7()).String()+"/run-requests", "", map[string]any{"reason": "x"}); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown template = %d %s, want 404", resp.Code, resp.Body)
	}
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, base+f.templateID.String()+"/run-requests", "", map[string]any{"reason": ""}); resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty reason = %d %s, want 422", resp.Code, resp.Body)
	}
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, base+f.templateID.String()+"/run-requests", "", map[string]any{"reason": "x", "host_id": f.hostB.String()}); resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("host outside the scope = %d %s, want 422", resp.Code, resp.Body)
	}
	// The token without jobs.request cannot ask.
	operator := f.memberID(t, "rr-valid-operator@example.test")
	readOnly := f.s.AssistantTokenFor(t, f.orgID, operator, tenancy.RoleOperator, tenancy.AreaJobsRead)
	if resp := f.s.AssistantDo(t, http.MethodPost, readOnly, base+f.templateID.String()+"/run-requests", "", map[string]any{"reason": "x"}); resp.Code != http.StatusForbidden {
		t.Fatalf("jobs.read token requesting = %d, want 403", resp.Code)
	}
}

func TestRunRequestReject(t *testing.T) {
	f := newRunRequestFixture(t, "rr-reject")
	created := f.request(t, map[string]any{"reason": "x"})
	resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID+"/reject", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	d := f.detail(t, created.Request.ID)
	if d.Request.Status != "rejected" {
		t.Fatalf("status = %s", d.Request.Status)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve after reject = %d, want 409", resp.StatusCode)
	}
}

func TestRunRequestExpires(t *testing.T) {
	clock := newTestClock()
	f := newRunRequestFixture(t, "rr-expire", apitest.WithRunRequestClock(clock.Now))
	created := f.request(t, map[string]any{"reason": "x"})
	d := f.detail(t, created.Request.ID)
	clock.advance(time.Hour)
	resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(mustReadBody(t, resp), "the request expired") {
		t.Fatalf("approve after an hour = %d, want 409 expired", resp.StatusCode)
	}
}

// TestRunRequestApprovalBindsToWhatWasSeen: any change to what the run would
// use, between the detail the person read and the click, refuses the
// approval (spec 2026-11-16 assistant access, decision 1b).
func TestRunRequestApprovalBindsToWhatWasSeen(t *testing.T) {
	cases := map[string]func(t *testing.T, f runRequestFixture){
		"template limit": func(t *testing.T, f runRequestFixture) {
			updateTemplate(t, f, func(in *templates.UpdateInput) { in.Limit = "web-*" })
		},
		"extra vars": func(t *testing.T, f runRequestFixture) {
			updateTemplate(t, f, func(in *templates.UpdateInput) { in.ExtraVars = map[string]any{"deploy_tag": "other"} })
		},
		"target added to the scope": func(t *testing.T, f runRequestFixture) {
			updateTemplate(t, f, func(in *templates.UpdateInput) {
				in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}}
			})
		},
		"template credential": func(t *testing.T, f runRequestFixture) {
			cred := f.hostCredential(t)
			updateTemplate(t, f, func(in *templates.UpdateInput) { in.CredentialID = &cred })
		},
		"playbook edit": func(t *testing.T, f runRequestFixture) {
			if _, err := f.s.Deps.Playbooks.UpdateEditor(f.scoped, f.orgID, f.playbook, playbooks.CreateInput{Inline: "- name: other\n  hosts: all\n  tasks: []\n"}); err != nil {
				t.Fatal(err)
			}
		},
		"host address": func(t *testing.T, f runRequestFixture) {
			h, err := f.s.Deps.Hosts.GetHost(f.scoped, f.orgID, f.hostA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Deps.Hosts.UpdateHost(f.scoped, f.orgID, f.hostA, h.Revision, hosts.HostInput{Name: h.Name, Address: "10.9.9.9", ZoneID: h.ZoneID, Managed: true}); err != nil {
				t.Fatal(err)
			}
		},
		"host ssh port": func(t *testing.T, f runRequestFixture) {
			cred := f.hostCredential(t)
			if _, err := f.s.Deps.SSH.SetHostSSH(f.scoped, f.orgID, f.hostA, sshconf.HostSSHInput{CredentialID: &cred, Port: 2222, BastionMode: sshconf.ModeInherit}); err != nil {
				t.Fatal(err)
			}
		},
		"group membership": func(t *testing.T, f runRequestFixture) {
			g, err := f.s.Deps.Inventory.CreateGroup(f.scoped, f.orgID, inventory.GroupInput{Name: "webservers"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.orgID, g.ID, f.hostA, nil, nil); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRunRequestFixture(t, "rr-bind-"+strings.ReplaceAll(name, " ", "-"))
			created := f.request(t, map[string]any{"reason": "x"})
			d := f.detail(t, created.Request.ID)
			change(t, f)
			resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
			body := mustReadBody(t, resp)
			if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "changed since you looked at it") {
				t.Fatalf("approve after %s = %d %s, want 409 changed", name, resp.StatusCode, body)
			}
			// Reloading and approving what is shown now works.
			if resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint); resp.StatusCode != http.StatusOK {
				t.Fatalf("approve after reloading = %d %s", resp.StatusCode, mustReadBody(t, resp))
			}
		})
	}
}

func TestRunRequestCancelOwnRunOnly(t *testing.T) {
	f := newRunRequestFixture(t, "rr-cancel")
	created := f.request(t, map[string]any{"reason": "x"})
	d := f.detail(t, created.Request.ID)
	resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	var approved struct {
		Request runRequestDTO `json:"request"`
	}
	decodeInto(t, resp, &approved)
	jobsResp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+approved.Request.RunGroupID, nil)
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, jobsResp, &list)
	own := list.Jobs[0].ID

	foreign := runJob(t, f.operatorC, f.runURL())[0].ID
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/jobs/"+foreign+"/cancel", "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("cancel a run the token did not request = %d %s, want 403", resp.Code, resp.Body)
	}
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/jobs/"+own+"/cancel", "", nil); resp.Code != http.StatusOK {
		t.Fatalf("cancel own run = %d %s", resp.Code, resp.Body)
	}
}

func updateTemplate(t *testing.T, f runRequestFixture, mutate func(*templates.UpdateInput)) {
	t.Helper()
	cur, err := f.s.Deps.Templates.Get(f.scoped, f.orgID, f.templateID)
	if err != nil {
		t.Fatal(err)
	}
	in := templates.UpdateInput{
		Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope, Limit: cur.Limit, ExtraVars: cur.ExtraVars,
		ExtraVarsSchema: cur.ExtraVarsSchema, CredentialID: cur.CredentialID, VaultCredentials: cur.VaultCredentials,
	}
	mutate(&in)
	if _, err := f.s.Deps.Templates.Update(f.scoped, f.orgID, f.templateID, in); err != nil {
		t.Fatal(err)
	}
}

func (f runFixture) hostCredential(t *testing.T) uuid.UUID {
	t.Helper()
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	ssh, err := f.s.Deps.SSH.HostSSH(scoped, f.orgID, f.hostA)
	if err != nil || ssh.CredentialID == nil {
		t.Fatalf("host ssh = %+v %v", ssh, err)
	}
	return *ssh.CredentialID
}

func (f runFixture) memberID(t *testing.T, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// testClock is read by the server's goroutines and moved by the test, so
// it is atomic.
type testClock struct{ nanos atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.nanos.Store(time.Now().UnixNano())
	return c
}

func (c *testClock) Now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *testClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

// TestApprovedRunStaysBoundUntilClaim: what a worker claims is still what
// the person approved. A change after the approval but before the claim
// (the assistant may still hold write areas) fails the job instead of
// running something nobody saw; an unchanged approved run claims normally.
func TestApprovedRunStaysBoundUntilClaim(t *testing.T) {
	cases := map[string]func(t *testing.T, f runRequestFixture){
		"template limit": func(t *testing.T, f runRequestFixture) {
			updateTemplate(t, f, func(in *templates.UpdateInput) { in.Limit = "web-*" })
		},
		"host address": func(t *testing.T, f runRequestFixture) {
			h, err := f.s.Deps.Hosts.GetHost(f.scoped, f.orgID, f.hostA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Deps.Hosts.UpdateHost(f.scoped, f.orgID, f.hostA, h.Revision, hosts.HostInput{Name: h.Name, Address: "203.0.113.9", ZoneID: h.ZoneID, Managed: true}); err != nil {
				t.Fatal(err)
			}
		},
		"nothing": nil,
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRunRequestFixture(t, "rr-claim-"+strings.ReplaceAll(name, " ", "-"))
			created := f.request(t, map[string]any{"reason": "x"})
			resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
			}
			if change != nil {
				change(t, f)
			}
			group := uuid.MustParse(f.detail(t, created.Request.ID).Request.RunGroupID)
			list, _, err := f.s.Deps.Jobs.List(f.scoped, f.orgID, jobs.Filter{RunGroupID: &group}, "", 10)
			if err != nil || len(list) == 0 {
				t.Fatalf("jobs of the approved run: %v %v", list, err)
			}
			claims := 0
			for range list {
				c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA, f.zoneB}, "v1")
				if err != nil {
					t.Fatal(err)
				}
				if c != nil {
					claims++
				}
			}
			if change == nil {
				if claims != len(list) {
					t.Fatalf("an unchanged approved run claimed %d of %d jobs", claims, len(list))
				}
				return
			}
			if claims != 0 {
				t.Fatalf("a run changed after its approval was claimed %d times", claims)
			}
			for _, j := range list {
				got, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, j.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != jobs.StatusError || !strings.Contains(got.Error, "changed after it was approved") {
					t.Fatalf("job after a change = %s %q, want error naming the change", got.Status, got.Error)
				}
			}
		})
	}
}

// withFollowUp gives the fixture's template one follow-up template, set
// through the template update route as an operator, and returns its id.
func (f runRequestFixture) withFollowUp(t *testing.T, name string, followUps ...uuid.UUID) uuid.UUID {
	t.Helper()
	fu := f.followUpTemplate(t, name, followUps...)
	resp := f.putTemplate(t, f.operatorC, f.templateID, "deploy", map[string]any{"follow_ups": []map[string]string{{"template_id": fu.String()}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set the template's follow-up = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	return fu
}

// setFollowUpsStart switches the organization setting as the admin.
func (f runRequestFixture) setFollowUpsStart(t *testing.T, on bool) {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.orgPath()+"/settings/assistant",
		map[string]any{"assistant_enabled": true, "approved_runs_start_follow_ups": on})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set the follow-up setting = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func (f runRequestFixture) approveWith(t *testing.T, id, fingerprint string, followUps []string) *http.Response {
	t.Helper()
	return postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id+"/approve", map[string]any{"fingerprint": fingerprint, "follow_ups": followUps})
}

type pictureFollowUpDTO struct {
	TemplateID       string               `json:"template_id"`
	TemplateName     string               `json:"template_name"`
	TemplateRevision int64                `json:"template_revision"`
	Stop             string               `json:"stop"`
	FollowUps        []pictureFollowUpDTO `json:"follow_ups"`
}

type pictureFollowUpsDTO struct {
	Start     bool                 `json:"start"`
	Templates []pictureFollowUpDTO `json:"templates"`
	Truncated bool                 `json:"truncated"`
}

func (d runRequestDetail) followUps(t *testing.T) *pictureFollowUpsDTO {
	t.Helper()
	var p struct {
		FollowUps *pictureFollowUpsDTO `json:"follow_ups"`
	}
	if err := json.Unmarshal(d.Picture, &p); err != nil {
		t.Fatal(err)
	}
	return p.FollowUps
}

// runFollowUps reads the chain block of an approved request's run: its
// follow-ups as template id -> state.
func (f runRequestFixture) runFollowUps(t *testing.T, requestID string) map[string]string {
	t.Helper()
	group := f.detail(t, requestID).Request.RunGroupID
	if group == "" {
		t.Fatal("the request has no run group")
	}
	resp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+group, nil)
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &list)
	if len(list.Jobs) == 0 {
		t.Fatalf("the approved request %s has no jobs", requestID)
	}
	out := map[string]string{}
	for _, fu := range f.chainDetail(t, list.Jobs[0].ID).Job.Chain.FollowUps {
		out[fu.TemplateID] = fu.State
	}
	return out
}

func (f runRequestFixture) approvalSummary(t *testing.T, requestID string) string {
	t.Helper()
	var summary string
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT summary FROM audit_events WHERE action = 'POST approveRunRequest' AND object_id = $1`, requestID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

func TestRunRequestFollowUpsOff(t *testing.T) {
	f := newRunRequestFixture(t, "rr-fu-off")
	fu := f.withFollowUp(t, "listmonk")
	created := f.request(t, map[string]any{"reason": "x"})
	d := f.detail(t, created.Request.ID)
	got := d.followUps(t)
	if got == nil || got.Start || len(got.Templates) != 1 || got.Templates[0].TemplateID != fu.String() || got.Templates[0].TemplateName != "listmonk" {
		t.Fatalf("picture follow-ups = %+v (%s)", got, d.Picture)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if rows := f.runFollowUps(t, created.Request.ID); len(rows) != 0 {
		t.Fatalf("follow-ups of a run approved with the setting off = %v", rows)
	}
	rev := f.getTemplate(t, f.templateID).Template.Revision
	if got, want := f.approvalSummary(t, created.Request.ID), "approved run request (template revision "+strconv.FormatInt(rev, 10)+"; no follow-ups)"; got != want {
		t.Fatalf("audit summary = %q, want %q", got, want)
	}
}

func TestRunRequestFollowUpsOn(t *testing.T) {
	f := newRunRequestFixture(t, "rr-fu-on")
	grandchild := f.followUpTemplate(t, "notify")
	fu := f.withFollowUp(t, "listmonk", grandchild)
	f.setFollowUpsStart(t, true)

	first := f.request(t, map[string]any{"reason": "x"})
	d := f.detail(t, first.Request.ID)
	got := d.followUps(t)
	if got == nil || !got.Start || len(got.Templates) != 1 {
		t.Fatalf("picture follow-ups = %+v (%s)", got, d.Picture)
	}
	node := got.Templates[0]
	if node.TemplateID != fu.String() || node.TemplateRevision == 0 || len(node.FollowUps) != 1 ||
		node.FollowUps[0].TemplateID != grandchild.String() || node.FollowUps[0].TemplateRevision == 0 {
		t.Fatalf("picture tree = %+v", node)
	}
	if resp := f.approve(t, f.adminC, first.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if rows := f.runFollowUps(t, first.Request.ID); len(rows) != 1 || rows[fu.String()] != "pending" {
		t.Fatalf("follow-ups of the default approval = %v, want listmonk pending", rows)
	}
	rev := strconv.FormatInt(f.getTemplate(t, f.templateID).Template.Revision, 10)
	if got, want := f.approvalSummary(t, first.Request.ID), "approved run request (template revision "+rev+"; follow-ups: listmonk)"; got != want {
		t.Fatalf("audit summary = %q, want %q", got, want)
	}

	second := f.request(t, map[string]any{"reason": "y"})
	if resp := f.approveWith(t, second.Request.ID, f.detail(t, second.Request.ID).Fingerprint, []string{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve deselected = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if rows := f.runFollowUps(t, second.Request.ID); len(rows) != 0 {
		t.Fatalf("follow-ups of an approval with none selected = %v", rows)
	}
	if got, want := f.approvalSummary(t, second.Request.ID), "approved run request (template revision "+rev+"; no follow-ups)"; got != want {
		t.Fatalf("audit summary = %q, want %q", got, want)
	}

	third := f.request(t, map[string]any{"reason": "z"})
	resp := f.approveWith(t, third.Request.ID, f.detail(t, third.Request.ID).Fingerprint, []string{grandchild.String()})
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "is not one this request would start") {
		t.Fatalf("approve with a foreign follow-up = %d %s, want 422", resp.StatusCode, body)
	}
	if st := f.detail(t, third.Request.ID).Request.Status; st != "pending" {
		t.Fatalf("status after a refused selection = %s, want pending", st)
	}
}

func TestRunRequestFollowUpSettingChangedRefuses(t *testing.T) {
	f := newRunRequestFixture(t, "rr-fu-flip")
	f.withFollowUp(t, "listmonk")
	created := f.request(t, map[string]any{"reason": "x"})
	d := f.detail(t, created.Request.ID)
	f.setFollowUpsStart(t, true)
	resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "changed since you looked at it") {
		t.Fatalf("approve after the setting flipped = %d %s, want 409", resp.StatusCode, body)
	}
}

// TestRunRequestReapprovalUsesItsOwnSelection: an approval whose enqueue
// failed leaves the request pending; the next approval starts what it
// selects, not what the failed one planned.
func TestRunRequestReapprovalUsesItsOwnSelection(t *testing.T) {
	f := newRunRequestFixture(t, "rr-fu-reapprove")
	f.withFollowUp(t, "listmonk")
	f.setFollowUpsStart(t, true)
	created := f.request(t, map[string]any{"reason": "x"})

	requirePins := func(on bool) {
		t.Helper()
		resp := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.orgPath()+"/settings/jobs", map[string]any{"jobs_require_pinned_hosts": on})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("set jobs_require_pinned_hosts = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
	}
	// Pinned host keys required and the only target unpinned: the enqueue
	// fails after the default approval planned its follow-up. (An
	// unmanaged target would fail the picture itself, not the enqueue.)
	requirePins(true)
	d := f.detail(t, created.Request.ID)
	resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode == http.StatusOK {
		t.Fatalf("approve with an unpinned target succeeded: %s", body)
	}
	if st := f.detail(t, created.Request.ID).Request.Status; st != "pending" {
		t.Fatalf("status after the failed enqueue = %s, want pending", st)
	}

	requirePins(false)
	if resp := f.approveWith(t, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint, []string{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("re-approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if rows := f.runFollowUps(t, created.Request.ID); len(rows) != 0 {
		t.Fatalf("follow-ups of the re-approval with none selected = %v", rows)
	}
}
