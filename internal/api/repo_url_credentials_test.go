package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestRepoDTOHidesURLPassword (backlog 7.103): a row stored before
// credentials were refused is returned to a viewer with the secret replaced,
// on the single read and the list alike, and in the instance admin's
// definition reads; the stored URL itself (the one a run's claim sends the
// worker) is left alone, and a new credential-bearing URL is a 400.
func TestRepoDTOHidesURLPassword(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("repo-url-owner@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, owner)
	org := s.SeedOrg("repo-url", owner)
	viewer := s.SeedUser("repo-url-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	base := s.HTTP.URL + "/api/v1/orgs/repo-url/repos"
	oc := s.LoginAs(owner)

	if resp := postJSON(t, oc, base, map[string]any{"name": "leaky", "url": "https://u:tok@git.example.com/x.git"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with a password: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := postJSON(t, oc, base, map[string]any{"name": "legacy", "url": "https://git.example.com/x.git"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created repoBody
	decodeInto(t, resp, &created)
	id := created.Repo.ID

	const stored = "https://u:tok@git.example.com/x.git"
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE git_repos SET url = $1 WHERE id = $2", stored, id); err != nil {
		t.Fatal(err)
	}

	vc := s.LoginAs(viewer)
	resp = requestJSON(t, vc, http.MethodGet, base+"/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	var got repoBody
	decodeInto(t, resp, &got)
	if got.Repo.URL != "https://***@git.example.com/x.git" {
		t.Fatalf("viewer GET url = %q, want the userinfo replaced by ***", got.Repo.URL)
	}
	resp = requestJSON(t, vc, http.MethodGet, base, nil)
	raw := mustReadBody(t, resp)
	if strings.Contains(raw, "tok") {
		t.Fatalf("viewer list leaks the token: %s", raw)
	}

	// An edit of another field, as the form sends it (the URL it was shown),
	// keeps the stored URL and the response stays redacted.
	resp = requestJSON(t, oc, http.MethodPut, base+"/"+id, map[string]any{"name": "legacy", "url": got.Repo.URL, "branch": "release"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT echoing the redacted URL: %d %s, want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &got)
	if got.Repo.URL != "https://***@git.example.com/x.git" || got.Repo.Branch != "release" {
		t.Fatalf("PUT response = %+v, want the redacted URL and the new branch", got.Repo)
	}

	rec, err := repos.NewSQLStore(s.DB).Get(db.WithOrgScope(context.Background(), org), org, uuid.MustParse(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.URL != stored {
		t.Fatalf("stored url = %q, want it untouched (the worker claim sends it as stored)", rec.URL)
	}

	// The definition reads redact as well.
	defs := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	if resp := postJSON(t, oc, defs, map[string]any{"name": "shared-leaky", "url": "https://tok@git.example.com/y.git"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create definition with a token: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = postJSON(t, oc, defs, map[string]any{"name": "shared-ok", "url": "https://git.example.com/y.git"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var def repoDefinitionDetailBody
	decodeInto(t, resp, &def)
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE shared_repos SET url = $1 WHERE id = $2", "https://u:tok@git.example.com/y.git", def.Definition.ID); err != nil {
		t.Fatal(err)
	}
	resp = requestJSON(t, oc, http.MethodGet, defs+"/"+def.Definition.ID, nil)
	decodeInto(t, resp, &def)
	if def.Definition.URL != "https://***@git.example.com/y.git" {
		t.Fatalf("definition url = %q, want the userinfo replaced by ***", def.Definition.URL)
	}
	raw = mustReadBody(t, requestJSON(t, oc, http.MethodGet, defs, nil))
	if strings.Contains(raw, "tok") {
		t.Fatalf("definition list leaks the token: %s", raw)
	}
}

// seedLeakyDefinition creates a shared definition through the API, then
// stores a credential-bearing URL on it and on its projection in org by SQL,
// the state a row stored before credentials were refused is in. It returns
// the definition's and the projection's ids.
func seedLeakyDefinition(t *testing.T, s *apitest.Server, c *http.Client, org uuid.UUID, stored string) (defID, projID string) {
	t.Helper()
	defs := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	resp := postJSON(t, c, defs, map[string]any{"name": "shared-echo", "url": "https://git.example.com/z.git", "branch": "main"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var def repoDefinitionDetailBody
	decodeInto(t, resp, &def)
	defID = def.Definition.ID
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE shared_repos SET url = $1 WHERE id = $2", stored, defID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Pool().QueryRow(context.Background(),
		"UPDATE git_repos SET url = $1 WHERE org_id = $2 AND shared_repo_id = $3 RETURNING id::text", stored, org, defID).Scan(&projID); err != nil {
		t.Fatalf("projection of the definition in the organization: %v", err)
	}
	return defID, projID
}

func storedDefinitionURL(t *testing.T, s *apitest.Server, defID string) string {
	t.Helper()
	var u string
	if err := s.DB.Pool().QueryRow(context.Background(), "SELECT url FROM shared_repos WHERE id = $1", defID).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

// TestProjectionUpdateEchoingRedactedURLKeepsIt (backlog 7.120): an
// organization's edit of its projection's own fields, as the form sends it
// with the redacted URL it was shown, is accepted and leaves the stored
// URLs alone.
func TestProjectionUpdateEchoingRedactedURLKeepsIt(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("proj-echo-owner@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, owner)
	org := s.SeedOrg("proj-echo", owner)
	oc := s.LoginAs(owner)
	const stored = "https://u:tok@git.example.com/z.git"
	defID, projID := seedLeakyDefinition(t, s, oc, org, stored)
	cred := seedVaultCredentialForRepo(t, s, org, "echo-vault")

	base := s.HTTP.URL + "/api/v1/orgs/proj-echo/repos/" + projID
	var got repoBody
	resp := requestJSON(t, oc, http.MethodGet, base, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	decodeInto(t, resp, &got)
	const redacted = "https://***@git.example.com/z.git"
	if got.Repo.URL != redacted {
		t.Fatalf("projection url = %q, want %q", got.Repo.URL, redacted)
	}

	resp = requestJSON(t, oc, http.MethodPut, base, map[string]any{
		"name": got.Repo.Name, "url": got.Repo.URL, "branch": got.Repo.Branch, "subpath": got.Repo.Subpath,
		"vault_identities": []map[string]any{{"credential_id": cred, "label": "prod"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("projection update echoing the redacted URL: %d %s, want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var after repoBody
	decodeInto(t, resp, &after)
	if after.Repo.URL != redacted || len(after.Repo.VaultIdentities) != 1 {
		t.Fatalf("update response = %+v, want the redacted URL and the new vault identity", after.Repo)
	}
	if u := storedDefinitionURL(t, s, defID); u != stored {
		t.Fatalf("stored definition url = %q, want it untouched", u)
	}
	rec, err := repos.NewSQLStore(s.DB).Get(db.WithOrgScope(context.Background(), org), org, uuid.MustParse(projID))
	if err != nil {
		t.Fatal(err)
	}
	if rec.URL != stored {
		t.Fatalf("stored projection url = %q, want it untouched", rec.URL)
	}
}

// TestSharedDefinitionPutEchoingRedactedURLKeepsIt (backlog 7.120): PUT of a
// definition with the redacted URL its GET showed, and a changed branch,
// answers 200, keeps the stored URL byte for byte and answers redacted.
func TestSharedDefinitionPutEchoingRedactedURLKeepsIt(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("def-echo-owner@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, owner)
	org := s.SeedOrg("def-echo", owner)
	oc := s.LoginAs(owner)
	const stored = "https://u:tok@git.example.com/z.git"
	defID, _ := seedLeakyDefinition(t, s, oc, org, stored)

	path := s.HTTP.URL + "/api/v1/admin/repos/definitions/" + defID
	var def repoDefinitionDetailBody
	decodeInto(t, requestJSON(t, oc, http.MethodGet, path, nil), &def)
	const redacted = "https://***@git.example.com/z.git"
	if def.Definition.URL != redacted {
		t.Fatalf("definition url = %q, want %q", def.Definition.URL, redacted)
	}

	resp := requestJSON(t, oc, http.MethodPut, path, map[string]any{
		"name": def.Definition.Name, "url": def.Definition.URL, "branch": "release", "subpath": def.Definition.Subpath,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT echoing the redacted URL: %d %s, want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &def)
	if def.Definition.URL != redacted || def.Definition.Branch != "release" {
		t.Fatalf("PUT response = %+v, want the redacted URL and the new branch", def.Definition)
	}
	if u := storedDefinitionURL(t, s, defID); u != stored {
		t.Fatalf("stored definition url = %q, want it byte-identical to %q", u, stored)
	}
}
