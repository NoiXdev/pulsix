package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type playbookDTOBody struct {
	Playbook struct {
		ID      string `json:"id"`
		Source  string `json:"source"`
		RepoID  string `json:"repo_id"`
		Path    string `json:"path"`
		Inline  string `json:"inline"`
		Summary string `json:"summary"`
	} `json:"playbook"`
}

const namedPlaybook = "- name: deploy site\n  hosts: all\n  tasks: []\n"

// seedEditorPlaybook stores one editor playbook through the service, outside
// any HTTP session.
func seedEditorPlaybook(t *testing.T, s *apitest.Server, orgID uuid.UUID, inline string) string {
	t.Helper()
	p, err := s.Deps.Playbooks.CreateEditor(db.WithOrgScope(context.Background(), orgID), orgID, playbooks.CreateInput{Inline: inline})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID.String()
}

// seedTemplatePlaybookFixture inserts one job template referencing a
// playbook, standing in for the templates service's rows (Task 7).
func seedTemplatePlaybook(t *testing.T, s *apitest.Server, orgID, playbookID uuid.UUID) {
	t.Helper()
	if _, err := s.DB.Pool().Exec(context.Background(),
		`INSERT INTO job_templates (id, org_id, name, playbook_id, scope)
		 VALUES ($1, $2, 'template', $3, $4)`,
		uuid.Must(uuid.NewV7()), orgID, playbookID,
		[]byte(`{"group_id": null, "host_ids": []}`)); err != nil {
		t.Fatal(err)
	}
}

func TestPlaybookLifecycle(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pb-owner@example.test", "correct horse battery")
	s.SeedOrg("pb-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/pb-org/playbooks"

	resp := postJSON(t, c, base, map[string]any{"name": "deploy", "inline": namedPlaybook})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created playbookDTOBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	pb := created.Playbook
	if pb.Source != "editor" || pb.Summary != "deploy site" || pb.Inline != namedPlaybook || pb.RepoID != "" || pb.Path != "" {
		t.Fatalf("created = %+v", pb)
	}

	resp = requestJSON(t, c, http.MethodGet, base, nil)
	list := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(list, "deploy site") {
		t.Fatalf("list: %d %s", resp.StatusCode, list)
	}
	// The list carries the summary, not the document itself.
	if strings.Contains(list, namedPlaybook) {
		t.Fatalf("the list must not carry full inline text: %s", list)
	}

	resp = requestJSON(t, c, http.MethodGet, base+"/"+pb.ID, nil)
	var got playbookDTOBody
	decodeInto(t, resp, &got)
	if resp.StatusCode != http.StatusOK || got.Playbook.Inline != namedPlaybook {
		t.Fatalf("get: %d %+v", resp.StatusCode, got)
	}

	// The summary is the first play's name; renaming the play moves it.
	resp = requestJSON(t, c, http.MethodPut, base+"/"+pb.ID, map[string]any{"inline": "- hosts: db\n  tasks: []\n"})
	var updated playbookDTOBody
	decodeInto(t, resp, &updated)
	if resp.StatusCode != http.StatusOK || updated.Playbook.Summary != "- hosts: db\n  tasks: []\n" {
		t.Fatalf("update: %d %+v", resp.StatusCode, updated)
	}

	resp = requestJSON(t, c, http.MethodDelete, base+"/"+pb.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, c, http.MethodGet, base+"/"+pb.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted: %d", resp.StatusCode)
	}
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/pb-org/activity", nil)
	activity := mustReadBody(t, resp)
	for _, want := range []string{"POST createPlaybook", "PUT updatePlaybook", "DELETE deletePlaybook"} {
		if !strings.Contains(activity, want) {
			t.Fatalf("activity missing %q: %s", want, activity)
		}
	}
}

func TestPlaybookRepoSource(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pbr-owner@example.test", "correct horse battery")
	org := s.SeedOrg("pbr-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/pbr-org/playbooks"
	repoID := seedRepoFixture(t, s, org, "infra")

	resp := postJSON(t, c, base, map[string]any{"repo_id": repoID, "path": "playbooks/site.yml"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create repo playbook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created playbookDTOBody
	decodeInto(t, resp, &created)
	if created.Playbook.Source != "repo" || created.Playbook.RepoID != repoID ||
		created.Playbook.Path != "playbooks/site.yml" || created.Playbook.Inline != "" {
		t.Fatalf("created = %+v", created.Playbook)
	}

	// A repo playbook is edited in its repository: PUT is refused.
	if resp := requestJSON(t, c, http.MethodPut, base+"/"+created.Playbook.ID, map[string]any{"inline": namedPlaybook}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("put repo playbook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/"+created.Playbook.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestPlaybookValidation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pb-val@example.test", "correct horse battery")
	org := s.SeedOrg("pb-val", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/pb-val/playbooks"
	repoID := seedRepoFixture(t, s, org, "infra")

	cases := []struct {
		name string
		body map[string]any
		want int
		msg  string
	}{
		{"malformed yaml", map[string]any{"inline": "- hosts: all\n\ttasks: []\n"}, http.StatusBadRequest, "malformed YAML"},
		{"not a list", map[string]any{"inline": "hosts: all\n"}, http.StatusBadRequest, "expected a list of plays"},
		{"missing hosts", map[string]any{"inline": "- name: x\n  tasks: []\n"}, http.StatusBadRequest, "play 1: missing hosts"},
		{"empty hosts", map[string]any{"inline": "- hosts: \"\"\n"}, http.StatusBadRequest, "play 1: missing hosts"},
		{"hosts not a string", map[string]any{"inline": "- hosts: 123\n"}, http.StatusBadRequest, "play 1: hosts must be a string"},
		{"oversized inline", map[string]any{"inline": strings.Repeat("x", 512*1024+1)}, http.StatusBadRequest, "playbook exceeds 512 KiB"},
		{"path with dotdot", map[string]any{"repo_id": repoID, "path": "playbooks/../site.yml"}, http.StatusBadRequest, "invalid path"},
		{"path without extension", map[string]any{"repo_id": repoID, "path": "playbooks/site"}, http.StatusBadRequest, "invalid path"},
		{"path bad chars", map[string]any{"repo_id": repoID, "path": "pl*ys.yml"}, http.StatusBadRequest, "invalid path"},
		{"unknown repo", map[string]any{"repo_id": "11111111-1111-1111-1111-111111111111", "path": "site.yml"}, http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, c, base, tc.body)
			body := mustReadBody(t, resp)
			if resp.StatusCode != tc.want || (tc.msg != "" && !strings.Contains(body, tc.msg)) {
				t.Fatalf("status %d, want %d (msg %q): %s", resp.StatusCode, tc.want, tc.msg, body)
			}
		})
	}

	// A repo playbook PUT stays a validation error; an unknown id 404s.
	resp := postJSON(t, c, base, map[string]any{"repo_id": repoID, "path": "site.yml"})
	var created playbookDTOBody
	decodeInto(t, resp, &created)
	if resp := requestJSON(t, c, http.MethodPut, base+"/11111111-1111-1111-1111-111111111111", map[string]any{"inline": namedPlaybook}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("put unknown playbook: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/11111111-1111-1111-1111-111111111111", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown playbook: %d", resp.StatusCode)
	}
}

func TestPlaybookInUseIsConflict(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pb-inuse@example.test", "correct horse battery")
	org := s.SeedOrg("pb-inuse", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/pb-inuse/playbooks"

	pbID := seedEditorPlaybook(t, s, org, namedPlaybook)
	seedTemplatePlaybook(t, s, org, uuid.MustParse(pbID))

	if resp := requestJSON(t, c, http.MethodDelete, base+"/"+pbID, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete referenced playbook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodGet, base+"/"+pbID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("referenced playbook must survive: %d", resp.StatusCode)
	}
}

func TestPlaybookPermissionsAndCrossOrgIsolation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("pbp-owner@example.test", "correct horse battery")
	org := s.SeedOrg("pbp-org", owner)
	operator := s.SeedUser("pbp-op@example.test", "correct horse battery")
	viewer := s.SeedUser("pbp-viewer@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	base := s.HTTP.URL + "/api/v1/orgs/pbp-org/playbooks"

	if resp := postJSON(t, s.Client(), base, map[string]any{"inline": namedPlaybook}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", resp.StatusCode)
	}

	pbID := seedEditorPlaybook(t, s, org, namedPlaybook)

	vc := s.LoginAs(viewer)
	if resp := requestJSON(t, vc, http.MethodGet, base, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list: %d", resp.StatusCode)
	}
	if resp := postJSON(t, vc, base, map[string]any{"inline": namedPlaybook}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, vc, http.MethodDelete, base+"/"+pbID, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer delete: %d", resp.StatusCode)
	}

	oc := s.LoginAs(operator)
	if resp := postJSON(t, oc, base, map[string]any{"inline": namedPlaybook}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator create: %d", resp.StatusCode)
	}

	// A playbook id from another org is invisible — exactly like one that
	// does not exist. The reader is a member of the other org (the owner
	// there), asking for this org's playbook id inside their own.
	outsider := s.SeedUser("pbp-out@example.test", "correct horse battery")
	s.SeedOrg("pbp-other", outsider)
	otherC := s.LoginAs(outsider)
	if resp := requestJSON(t, otherC, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/pbp-other/playbooks/"+pbID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org playbook read: %d", resp.StatusCode)
	}
}
