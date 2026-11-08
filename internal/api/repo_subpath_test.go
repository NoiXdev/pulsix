package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestRepoSubpathIsStoredCanonical (backlog 7.94): an organization's
// repository and an installation's definition both answer — and store —
// "/ansible/" as "ansible", the form a run's claim needs; a definition's
// traversal is refused with 400 like a repository's always was.
func TestRepoSubpathIsStoredCanonical(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("subpath-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	s.SeedOrg("subpath-org", admin)
	c := s.LoginAs(admin)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/subpath-org/repos", map[string]any{
		"name": "own", "url": "https://git.example.com/own.git", "subpath": "/ansible/",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create repo: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var repo repoBody
	decodeInto(t, resp, &repo)
	if repo.Repo.Subpath != "ansible" {
		t.Fatalf("repo subpath = %q, want %q", repo.Repo.Subpath, "ansible")
	}

	defsBase := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	resp = postJSON(t, c, defsBase, map[string]any{
		"name": "shared", "url": "https://git.example.com/shared.git", "subpath": "./deploy//ansible/",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var def repoDefinitionDetailBody
	decodeInto(t, resp, &def)
	if def.Definition.Subpath != "deploy/ansible" {
		t.Fatalf("definition subpath = %q, want %q", def.Definition.Subpath, "deploy/ansible")
	}

	resp = requestJSON(t, c, http.MethodPut, defsBase+"/"+def.Definition.ID, map[string]any{
		"name": "shared", "url": "https://git.example.com/shared.git", "subpath": "../outside",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("definition with traversal: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
}
