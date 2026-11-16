package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/templates"
)

// followUpTemplate creates a template scoped to the fixture's first host.
func (f runFixture) followUpTemplate(t *testing.T, name string, followUps ...uuid.UUID) uuid.UUID {
	t.Helper()
	tm, err := f.s.Deps.Templates.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, templates.CreateInput{
		Name: name, PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.hostA}}, FollowUps: followUps,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tm.ID
}

func (f runFixture) templateURL(id uuid.UUID) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/job-templates/" + id.String()
}

type followUpTemplateDTO struct {
	Template struct {
		ID        string `json:"id"`
		Revision  int64  `json:"revision"`
		FollowUps []struct {
			TemplateID   string `json:"template_id"`
			TemplateName string `json:"template_name"`
		} `json:"follow_ups"`
	} `json:"template"`
}

func (f runFixture) getTemplate(t *testing.T, id uuid.UUID) followUpTemplateDTO {
	t.Helper()
	resp := requestJSON(t, f.operatorC, http.MethodGet, f.templateURL(id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get template: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out followUpTemplateDTO
	decodeInto(t, resp, &out)
	return out
}

func (f runFixture) putTemplate(t *testing.T, c *http.Client, id uuid.UUID, name string, extra map[string]any) *http.Response {
	t.Helper()
	body := map[string]any{"name": name, "playbook_id": f.playbook, "scope": map[string]any{"host_ids": []string{f.hostA.String()}}}
	for k, v := range extra {
		body[k] = v
	}
	return requestJSON(t, c, http.MethodPut, f.templateURL(id), body)
}

func TestTemplateFollowUpsRoundTrip(t *testing.T) {
	f := newRunFixture(t, "tfu-roundtrip")
	b := f.followUpTemplate(t, "notify")

	resp := postJSON(t, f.operatorC, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/job-templates", map[string]any{
		"name": "build", "playbook_id": f.playbook, "scope": map[string]any{"host_ids": []string{f.hostA.String()}},
		"follow_ups": []map[string]string{{"template_id": b.String()}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created followUpTemplateDTO
	decodeInto(t, resp, &created)
	a := uuid.MustParse(created.Template.ID)
	if len(created.Template.FollowUps) != 1 || created.Template.FollowUps[0].TemplateName != "notify" {
		t.Fatalf("created follow-ups = %+v", created.Template.FollowUps)
	}
	rev := created.Template.Revision

	// A body without the field keeps the list.
	if resp := f.putTemplate(t, f.operatorC, a, "build", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("put keep: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got := f.getTemplate(t, a)
	if len(got.Template.FollowUps) != 1 || got.Template.FollowUps[0].TemplateID != b.String() {
		t.Fatalf("after PUT without the field: %+v", got.Template.FollowUps)
	}
	// An empty list clears it and the revision moves on.
	if resp := f.putTemplate(t, f.operatorC, a, "build", map[string]any{"follow_ups": []any{}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put clear: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got = f.getTemplate(t, a)
	if len(got.Template.FollowUps) != 0 {
		t.Fatalf("after PUT []: %+v", got.Template.FollowUps)
	}
	if got.Template.Revision <= rev {
		t.Fatalf("revision %d did not move past %d", got.Template.Revision, rev)
	}
	// A template without follow-ups reads an empty list, not null.
	if body := mustReadBody(t, requestJSON(t, f.operatorC, http.MethodGet, f.templateURL(b), nil)); !strings.Contains(body, `"follow_ups":[]`) {
		t.Fatalf("follow_ups must be [] when none: %s", body)
	}
}

func TestTemplateFollowUpCycle422(t *testing.T) {
	f := newRunFixture(t, "tfu-cycle")
	b := f.followUpTemplate(t, "listmonk")
	a := f.followUpTemplate(t, "cloudpanel", b)

	resp := f.putTemplate(t, f.operatorC, b, "listmonk", map[string]any{"follow_ups": []map[string]string{{"template_id": a.String()}}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("cycle: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var problem struct {
		Detail string `json:"detail"`
		Errors []struct {
			Location string `json:"location"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	decodeInto(t, resp, &problem)
	const want = "follow-ups would form a cycle: listmonk → cloudpanel → listmonk"
	if problem.Detail != want {
		t.Fatalf("detail = %q, want %q", problem.Detail, want)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "body.follow_ups" || problem.Errors[0].Message != want {
		t.Fatalf("errors = %+v", problem.Errors)
	}
}

func TestTemplateDeleteReferenced409(t *testing.T) {
	f := newRunFixture(t, "tfu-delete")
	b := f.followUpTemplate(t, "notify")
	f.followUpTemplate(t, "build", b)

	resp := requestJSON(t, f.operatorC, http.MethodDelete, f.templateURL(b), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	decodeInto(t, resp, &problem)
	if problem.Detail != "this template is a follow-up of: build" {
		t.Fatalf("detail = %q", problem.Detail)
	}
}

// The operator role holds both templates.write and jobs.run, and a token
// can only narrow a role, so no principal can hold the first without the
// second today: the jobs.run check on follow-ups cannot be exercised until
// the permission matrix splits them. The viewer case below covers the
// templates.write half of the rule.
func TestFollowUpsNeedJobsRun(t *testing.T) {
	t.Skip("no role holds templates.write without jobs.run in the current permission matrix")
}

func TestTemplateFollowUpsNeedTemplatesWrite(t *testing.T) {
	f := newRunFixture(t, "tfu-viewer")
	b := f.followUpTemplate(t, "notify")
	a := f.followUpTemplate(t, "build")
	resp := f.putTemplate(t, f.viewerC, a, "build", map[string]any{"follow_ups": []map[string]string{{"template_id": b.String()}}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: %d", resp.StatusCode)
	}
}
