package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// followUpRows reads the run_follow_ups rows of a parent run group as
// template -> state.
func (f runFixture) followUpRows(t *testing.T, group string) map[uuid.UUID]string {
	t.Helper()
	rows, err := f.s.DB.Pool().Query(context.Background(),
		`SELECT template_id, state FROM run_follow_ups WHERE org_id = $1 AND parent_group_id = $2`, f.orgID, uuid.MustParse(group))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			t.Fatal(err)
		}
		out[id] = state
	}
	return out
}

type chainDetail struct {
	Job struct {
		Trigger string `json:"trigger"`
		Chain   struct {
			Parent *struct {
				GroupID      string `json:"group_id"`
				FirstJobID   string `json:"first_job_id"`
				TemplateID   string `json:"template_id"`
				TemplateName string `json:"template_name"`
			} `json:"parent"`
			FollowUps []struct {
				ID              string   `json:"id"`
				TemplateID      string   `json:"template_id"`
				TemplateName    string   `json:"template_name"`
				State           string   `json:"state"`
				Depth           int      `json:"depth"`
				Reason          string   `json:"reason"`
				ChildFirstJobID string   `json:"child_first_job_id"`
				Hosts           []string `json:"hosts"`
			} `json:"follow_ups"`
		} `json:"chain"`
	} `json:"job"`
}

func (f runFixture) chainDetail(t *testing.T, jobID string) chainDetail {
	t.Helper()
	resp := requestJSON(t, f.operatorC, http.MethodGet, f.baseURL()+"/"+jobID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out chainDetail
	decodeInto(t, resp, &out)
	return out
}

func TestRunWithFollowUpOverride(t *testing.T) {
	f := newRunFixture(t, "chain-override")
	b := f.followUpTemplate(t, "notify")
	c := f.followUpTemplate(t, "cleanup")
	withDefault := f.followUpTemplate(t, "build", c)

	// The template's own follow-ups apply without an override.
	job := runJob(t, f.operatorC, f.runURLFor(withDefault))[0]
	if got := f.followUpRows(t, job.RunGroupID); len(got) != 1 || got[c] != "pending" {
		t.Fatalf("default rows = %v", got)
	}
	// An override replaces them.
	resp := postJSON(t, f.operatorC, f.runURLFor(withDefault), map[string]any{"follow_ups": []string{b.String()}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("override: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var started struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &started)
	if got := f.followUpRows(t, started.Jobs[0].RunGroupID); len(got) != 1 || got[b] != "pending" {
		t.Fatalf("override rows = %v", got)
	}
	// An empty override writes none.
	resp = postJSON(t, f.operatorC, f.runURLFor(withDefault), map[string]any{"follow_ups": []string{}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("empty override: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &started)
	if got := f.followUpRows(t, started.Jobs[0].RunGroupID); len(got) != 0 {
		t.Fatalf("empty override rows = %v", got)
	}
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST runJobTemplate", "job_template", withDefault.String())
}

func TestRunFollowUpOverrideRejects(t *testing.T) {
	f := newRunFixture(t, "chain-reject")
	resp := postJSON(t, f.operatorC, f.runURL(), map[string]any{"follow_ups": []string{f.templateID.String()}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if body := mustReadBody(t, resp); !strings.Contains(body, `"location":"body.follow_ups"`) {
		t.Fatalf("self: want the error at body.follow_ups: %s", body)
	}
	resp = postJSON(t, f.operatorC, f.runURL(), map[string]any{"follow_ups": []string{uuid.NewString()}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = postJSON(t, f.operatorC, f.runURL(), map[string]any{"follow_ups": []string{"nope"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestJobDetailChainBlock(t *testing.T) {
	f := newRunFixture(t, "chain-detail")
	b := f.followUpTemplate(t, "notify")
	parentTmpl := f.followUpTemplate(t, "build", b)

	parent := runJob(t, f.operatorC, f.runURLFor(parentTmpl))[0]
	d := f.chainDetail(t, parent.ID)
	if d.Job.Chain.Parent != nil {
		t.Fatalf("a manual run has no parent: %+v", d.Job.Chain.Parent)
	}
	if len(d.Job.Chain.FollowUps) != 1 || d.Job.Chain.FollowUps[0].State != "pending" || d.Job.Chain.FollowUps[0].TemplateName != "notify" {
		t.Fatalf("follow-ups = %+v", d.Job.Chain.FollowUps)
	}

	finishJob(t, f.s, f.zoneA, uuid.MustParse(parent.ID), jobs.StatusSucceeded, map[string]any{"ok_hosts": []string{f.hostA.String()}}, "")
	if err := f.s.ChainStarter.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	d = f.chainDetail(t, parent.ID)
	fu := d.Job.Chain.FollowUps[0]
	if fu.State != "started" || fu.ChildFirstJobID == "" || len(fu.Hosts) != 1 || fu.Hosts[0] != f.hostA.String() {
		t.Fatalf("parent follow-up = %+v", fu)
	}
	child := f.chainDetail(t, fu.ChildFirstJobID)
	if child.Job.Trigger != "chain" {
		t.Fatalf("trigger = %q, want chain", child.Job.Trigger)
	}
	p := child.Job.Chain.Parent
	if p == nil || p.FirstJobID != parent.ID || p.GroupID != parent.RunGroupID || p.TemplateName != "build" || p.TemplateID != parentTmpl.String() {
		t.Fatalf("child parent = %+v", p)
	}
	if child.Job.Chain.FollowUps == nil || len(child.Job.Chain.FollowUps) != 0 {
		t.Fatalf("child follow-ups = %+v", child.Job.Chain.FollowUps)
	}
	// The trigger filter knows the new kind.
	resp := requestJSON(t, f.operatorC, http.MethodGet, f.baseURL()+"?trigger=chain", nil)
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, resp, &list)
	if len(list.Jobs) != 1 || list.Jobs[0].ID != fu.ChildFirstJobID {
		t.Fatalf("trigger=chain list = %+v", list.Jobs)
	}
}

func TestDismissFollowUps(t *testing.T) {
	f := newRunFixture(t, "chain-dismiss")
	b := f.followUpTemplate(t, "notify")
	parentTmpl := f.followUpTemplate(t, "build", b)
	parent := runJob(t, f.operatorC, f.runURLFor(parentTmpl))[0]
	url := f.baseURL() + "/" + parent.ID + "/follow-ups/dismiss"

	if resp := postJSON(t, f.viewerC, url, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: %d", resp.StatusCode)
	}
	resp := postJSON(t, f.operatorC, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Dismissed int `json:"dismissed"`
	}
	decodeInto(t, resp, &out)
	if out.Dismissed != 1 {
		t.Fatalf("dismissed = %d", out.Dismissed)
	}
	d := f.chainDetail(t, parent.ID)
	if fu := d.Job.Chain.FollowUps[0]; fu.State != "dismissed" || fu.Reason != "dismissed by "+"chain-dismiss-operator@example.test" {
		t.Fatalf("follow-up = %+v", fu)
	}
	assertAuditEvent(t, f.s, f.adminC, f.orgSlug, "POST dismissFollowUps", "job", parent.ID)

	// Once the run is terminal there is nothing left to dismiss.
	finishJob(t, f.s, f.zoneA, uuid.MustParse(parent.ID), jobs.StatusSucceeded, nil, "")
	if resp := postJSON(t, f.operatorC, url, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("terminal: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// The parent's first job is the lowest id among jobs sharing created_at,
// the order the rest of the system uses (created_at, id).
func TestJobDetailParentFirstJobBreaksTiesByLowestID(t *testing.T) {
	f := newRunFixture(t, "chain-tie")
	b := f.followUpTemplate(t, "notify")
	parentTmpl, err := f.s.Deps.Templates.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, templates.CreateInput{
		Name: "build", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}}, FollowUps: []uuid.UUID{b},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := runJob(t, f.operatorC, f.runURLFor(parentTmpl.ID))
	if len(created) != 2 {
		t.Fatalf("want one job per zone, got %d", len(created))
	}
	if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE jobs SET created_at = now() WHERE run_group_id = $1`, uuid.MustParse(created[0].RunGroupID)); err != nil {
		t.Fatal(err)
	}
	lowest := created[0].ID
	for _, j := range created {
		if j.ID < lowest {
			lowest = j.ID
		}
	}
	for _, j := range created {
		zone := f.zoneA
		if j.ZoneID == f.zoneB.String() {
			zone = f.zoneB
		}
		host := f.hostA
		if zone == f.zoneB {
			host = f.hostB
		}
		finishJob(t, f.s, zone, uuid.MustParse(j.ID), jobs.StatusSucceeded, map[string]any{"ok_hosts": []string{host.String()}}, "")
	}
	if err := f.s.ChainStarter.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	parent := f.chainDetail(t, created[0].ID)
	child := f.chainDetail(t, parent.Job.Chain.FollowUps[0].ChildFirstJobID)
	if p := child.Job.Chain.Parent; p == nil || p.FirstJobID != lowest {
		t.Fatalf("parent first job = %+v, want the lowest id %s", p, lowest)
	}
	if d := parent.Job.Chain.FollowUps[0].Depth; d != 1 {
		t.Fatalf("depth = %d, want 1", d)
	}
}
