package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
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
