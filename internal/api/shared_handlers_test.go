package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type repoDefinitionDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	Subpath string `json:"subpath"`
}

type repoDefinitionDetailBody struct {
	Definition repoDefinitionDTO `json:"definition"`
}

type repoDefinitionListBody struct {
	Definitions []repoDefinitionDTO `json:"definitions"`
}

// markedRepoBody decodes just enough of GET/POST .../repos' own repoDTO
// (repo_handlers.go) to see Task 4 Step 3's marker: SharedRepoID, set only
// on a projection.
type markedRepoBody struct {
	Repo struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Branch       string `json:"branch"`
		SharedRepoID string `json:"shared_repo_id"`
	} `json:"repo"`
}

type markedRepoListBody struct {
	Repos []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Branch       string `json:"branch"`
		SharedRepoID string `json:"shared_repo_id"`
	} `json:"repos"`
}

// makeInstanceAdmin flips the flag directly, the same shortcut
// TestDeactivateUserIsInstanceAdminOnly and
// TestSMTPSettingsRoutesRequireInstanceAdmin's sibling tests already use —
// there is no API route to grant it (spec decision 8: an instance admin is
// a precondition these routes check, never a role they hand out).
func makeInstanceAdmin(t *testing.T, s *apitest.Server, userID uuid.UUID) {
	t.Helper()
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE users SET instance_admin = true WHERE id = $1", userID.String()); err != nil {
		t.Fatal(err)
	}
}

// TestRepoDefinitionLifecycleAndProjectionMarker covers Task 4 Steps 1 and
// 3 end to end: an instance admin's CRUD over a repository definition, its
// immediate projection into an existing organization (spec v0.2f decision
// 4, Task 3's wiring), the shared_repo_id marker Step 3 adds to that
// organization's own repoDTO, and that the projection still refuses an
// organization's own attempt to change a shared field or delete it
// (decisions 2 and 6, already built by Task 3 — exercised here for the
// first time through the real HTTP routes rather than repos' own unit
// tests).
func TestRepoDefinitionLifecycleAndProjectionMarker(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("def-lifecycle-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("def-lifecycle-owner@example.test", "correct horse battery")
	s.SeedOrg("def-lifecycle-org", owner)
	ownerClient := s.LoginAs(owner)

	defsBase := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	resp := postJSON(t, adminClient, defsBase, map[string]any{
		"name": "infra-shared", "url": "https://git.example.com/acme/infra.git", "branch": "main",
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create definition: %d %s", resp.StatusCode, body)
	}
	var created repoDefinitionDetailBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID
	if defID == "" || created.Definition.Branch != "main" {
		t.Fatalf("created definition = %+v, want a non-empty id and branch main", created.Definition)
	}

	// It appears in the list and GET /{id}.
	resp = requestJSON(t, adminClient, http.MethodGet, defsBase, nil)
	var list repoDefinitionListBody
	decodeInto(t, resp, &list)
	found := false
	for _, d := range list.Definitions {
		if d.ID == defID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list definitions = %+v, want it to contain %s", list.Definitions, defID)
	}
	resp = requestJSON(t, adminClient, http.MethodGet, defsBase+"/"+defID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The already-existing organization got a projection immediately (spec
	// decision 4), marked with shared_repo_id (Task 4 Step 3).
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/def-lifecycle-org/repos", nil)
	var repos markedRepoListBody
	decodeInto(t, resp, &repos)
	if len(repos.Repos) != 1 {
		t.Fatalf("org repos = %+v, want exactly one projection", repos.Repos)
	}
	projected := repos.Repos[0]
	if projected.SharedRepoID != defID {
		t.Fatalf("projected.SharedRepoID = %q, want %s", projected.SharedRepoID, defID)
	}
	if projected.Name != "infra-shared" || projected.Branch != "main" {
		t.Fatalf("projected = %+v, want name infra-shared, branch main", projected)
	}

	// Editing the definition propagates (spec decision 2) — the
	// organization's own branch changes with no action on its part.
	resp = requestJSON(t, adminClient, http.MethodPut, defsBase+"/"+defID, map[string]any{
		"name": "infra-shared", "url": "https://git.example.com/acme/infra.git", "branch": "develop",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/def-lifecycle-org/repos/"+projected.ID, nil)
	var afterEdit markedRepoBody
	decodeInto(t, resp, &afterEdit)
	if afterEdit.Repo.Branch != "develop" {
		t.Fatalf("org's own repo branch after admin edit = %q, want develop (propagation)", afterEdit.Repo.Branch)
	}

	// The organization cannot change a shared field itself (Task 3,
	// decision 2) ...
	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/def-lifecycle-org/repos/"+projected.ID, map[string]any{
		"name": "infra-shared", "url": "https://git.example.com/attacker/infra.git", "branch": "develop",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("org changing a shared field: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	// ... nor delete the projection itself (Task 3, decision 6).
	resp = requestJSON(t, ownerClient, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/def-lifecycle-org/repos/"+projected.ID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("org deleting its own projection: %d %s, want 403", resp.StatusCode, mustReadBody(t, resp))
	}

	// Deleting the definition takes the projection with it (spec decision
	// 5: nothing references it yet, so this succeeds outright).
	resp = requestJSON(t, adminClient, http.MethodDelete, defsBase+"/"+defID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/def-lifecycle-org/repos", nil)
	var afterDelete markedRepoListBody
	decodeInto(t, resp, &afterDelete)
	if len(afterDelete.Repos) != 0 {
		t.Fatalf("org repos after definition delete = %+v, want none left", afterDelete.Repos)
	}
}

// TestRepoDefinitionRoutesRequireInstanceAdmin covers Task 4 Step 1's own
// warning: testing only the first verb is the hole this kind of gate
// usually has. A plain member of some organization (not an instance admin)
// is refused on every one of the five verbs, individually.
func TestRepoDefinitionRoutesRequireInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	member := s.SeedUser("def-admin-gate-member@example.test", "correct horse battery")
	s.SeedOrg("def-admin-gate-org", member)
	c := s.LoginAs(member)
	base := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	someID := "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		name   string
		method string
		url    string
		body   any
	}{
		{"list", http.MethodGet, base, nil},
		{"create", http.MethodPost, base, map[string]any{"name": "x", "url": "https://git.example.com/x"}},
		{"get", http.MethodGet, base + "/" + someID, nil},
		{"update", http.MethodPut, base + "/" + someID, map[string]any{"name": "x", "url": "https://git.example.com/x"}},
		{"delete", http.MethodDelete, base + "/" + someID, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := requestJSON(t, c, tc.method, tc.url, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as non-admin: status %d, want 403; body %s", tc.name, resp.StatusCode, mustReadBody(t, resp))
			}
		})
	}
}

// TestRepoDefinitionCollisionNamesOrganizationToInstanceAdmin is Task 4's
// Review Focus 3, the positive half: spec decision 7 says a name collision
// must fail loudly AND name the organization, to an instance admin. Written
// against a deliberately naive mapSharedErr first (passing
// *shared.NameCollisionError's own Error() straight through, exactly the
// shape the brief warns is tempting) and confirmed to fail — that error
// string states only a count by Task 2's own design ("name %q is already
// used by %d organization(s)"), never a name, so the naive mapping under-
// delivers on this requirement even though it happens not to leak. Restored
// to the careful version (shared_handlers.go's mapSharedErr, which resolves
// *shared.NameCollisionError.Orgs to slugs via tenancy.Service.GetOrgByID)
// to pass. See TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity below for
// why naming the organization here, specifically, must never be reachable
// from anywhere else.
func TestRepoDefinitionCollisionNamesOrganizationToInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("def-collision-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	victimOwner := s.SeedUser("def-collision-victim-owner@example.test", "correct horse battery")
	s.SeedOrg("collision-victim-org", victimOwner)
	victimClient := s.LoginAs(victimOwner)

	// The victim organization already has its own, ordinary repository
	// named "ansible-base" — nothing to do with any definition.
	resp := postJSON(t, victimClient, s.HTTP.URL+"/api/v1/orgs/collision-victim-org/repos", map[string]any{
		"name": "ansible-base", "url": "https://git.example.com/victim/infra.git",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed victim org's own repo: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The instance admin now defines a shared repository with that same
	// name — spec decision 7: refused, naming the organization.
	resp = postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/repos/definitions", map[string]any{
		"name": "ansible-base", "url": "https://git.example.com/provider/infra.git",
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("create colliding definition: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(body, "collision-victim-org") {
		t.Fatalf("collision response body = %q, want it to name organization collision-victim-org", body)
	}
}

// TestRepoDefinitionInUseNamesOrganizationToInstanceAdmin covers the other
// half the brief calls out explicitly: "*shared.InUseError carries an OrgID
// for the same reason, and the same rule applies to it" — spec decision 5's
// delete refusal must also name the organization to an instance admin.
func TestRepoDefinitionInUseNamesOrganizationToInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("def-inuse-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("def-inuse-owner@example.test", "correct horse battery")
	s.SeedOrg("inuse-victim-org", owner)
	ownerClient := s.LoginAs(owner)

	defsBase := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	resp := postJSON(t, adminClient, defsBase, map[string]any{
		"name": "triggered-repo", "url": "https://git.example.com/provider/infra.git",
	})
	var created repoDefinitionDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/inuse-victim-org/repos", nil)
	var repos markedRepoListBody
	decodeInto(t, resp, &repos)
	if len(repos.Repos) != 1 {
		t.Fatalf("org repos = %+v, want exactly one projection", repos.Repos)
	}
	projectedID := repos.Repos[0].ID

	// A job trigger on the projection is customer configuration (spec
	// decision 5's own wording: "a schedule, a trigger or a job"), created
	// through the real org-scoped route exactly as an organization would.
	resp = postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/inuse-victim-org/triggers", map[string]any{
		"kind": "repo_sync", "repo_id": projectedID, "name": "sync-hook", "preset": "pulsix",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed job trigger: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, adminClient, http.MethodDelete, defsBase+"/"+defID, nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete definition in use: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(body, "inuse-victim-org") {
		t.Fatalf("in-use response body = %q, want it to name organization inuse-victim-org", body)
	}
}

// TestRepoDefinitionInUseByTemplateDefinitionReturns409NamingIt is a
// whole-branch review's own finding, through the real HTTP stack:
// shared_job_templates.shared_repo_id is NOT NULL REFERENCES shared_repos
// ON DELETE RESTRICT, and this dependant lives at the INSTALLATION level —
// a template DEFINITION, not anything inside an organization, so no
// organization needs to exist at all to reproduce it. Before the fix, this
// DELETE hit that RESTRICT with no sentinel attached and mapSharedErr's
// unmapped-error branch returned a bare 500; the fix gives it the same
// typed, named refusal decision 5 promises for every other dependant.
func TestRepoDefinitionInUseByTemplateDefinitionReturns409NamingIt(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("def-inuse-template-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	reposBase := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	resp := postJSON(t, adminClient, reposBase, map[string]any{
		"name": "infra-blocked-by-template", "url": "https://git.example.com/provider/infra.git",
	})
	var repoCreated repoDefinitionDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &repoCreated); err != nil {
		t.Fatal(err)
	}
	repoDefID := repoCreated.Definition.ID

	resp = postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/templates/definitions", map[string]any{
		"name": "deploy-site", "shared_repo_id": repoDefID, "playbook_path": "site.yml",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create shared template definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, adminClient, http.MethodDelete, reposBase+"/"+repoDefID, nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete repo definition a template definition references: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(body, "deploy-site") {
		t.Fatalf("in-use response body = %q, want it to name the blocking template definition deploy-site", body)
	}

	// Refused, not removed: a follow-up GET must still find it.
	resp = requestJSON(t, adminClient, http.MethodGet, reposBase+"/"+repoDefID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get repo definition after a refused delete: %d %s, want 200 (it must still exist)", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity is Task 4's Review
// Focus 3, the hard half: a member of organization A provokes every
// organization-scoped repository path reachable from org A, and none of
// the response bodies may ever mention organization B's slug, name, or raw
// id — decision 7's collision message (and decision 5's in-use message) is
// correct only when an instance admin, who already sees every
// organization, reads it; anywhere else it is one customer learning
// another customer's identity. The id matters as much as the slug: the
// likeliest real leak shape is *shared.InUseError.Error(), which embeds the
// raw OrgID (internal/shared/definition.go) rather than the slug, so a
// check that only forbids the slug would walk straight past it.
//
// This asserts on response body text throughout, not status codes — a 409
// or a 403 is an expected, correct outcome for several of these calls (an
// intra-org name clash, a non-admin reaching the instance-admin routes);
// what must never be true is that ANY of those bodies names organization B.
//
// Verified rather than assumed: internal/repos (service.go, store_sql.go)
// imports nothing from internal/shared, so none of the organization-scoped
// routes below can produce a *shared.NameCollisionError or *shared.InUseError
// today — there is no live path to drive this test red through the full
// HTTP stack without first reintroducing the exact mistake this test exists
// to catch. That mistake was reintroduced, deliberately, at the one layer
// that could actually carry it (mapReposErr, the error mapper every one of
// these routes goes through): see TestMapReposErrNeverNamesAnOrganization
// (shared_handlers_internal_test.go, package api) for the real red/green
// cycle against a naive case added there and removed again.
func TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("leak-org-a-owner@example.test", "correct horse battery")
	orgASlug := "leak-org-a"
	orgA := s.SeedOrg(orgASlug, ownerA)
	operatorA := s.SeedUser("leak-org-a-operator@example.test", "correct horse battery")
	s.AddMember(orgA, operatorA, tenancy.RoleOperator)
	operatorClient := s.LoginAs(operatorA)

	ownerB := s.SeedUser("leak-org-b-owner@example.test", "correct horse battery")
	const orgBIdentity = "leak-secret-org-b" // SeedOrg sets both slug and name to this string.
	orgB := s.SeedOrg(orgBIdentity, ownerB)
	bClient := s.LoginAs(ownerB)
	// Org B owns a repository of the same name org A is about to create —
	// a real, same-named row elsewhere, so a leak would have something
	// concrete to leak, not just an organization identity with nothing
	// behind it.
	if resp := postJSON(t, bClient, s.HTTP.URL+"/api/v1/orgs/"+orgBIdentity+"/repos", map[string]any{
		"name": "shared-candidate", "url": "https://git.example.com/b/infra.git",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed org B's repo: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	base := s.HTTP.URL + "/api/v1/orgs/" + orgASlug + "/repos"
	var bodies []string
	capture := func(label string, resp *http.Response) string {
		t.Helper()
		body := mustReadBody(t, resp)
		bodies = append(bodies, body)
		t.Logf("%s: %d %s", label, resp.StatusCode, body)
		return body
	}

	capture("list (empty)", requestJSON(t, operatorClient, http.MethodGet, base, nil))

	createResp := postJSON(t, operatorClient, base, map[string]any{"name": "repo1", "url": "https://git.example.com/a/infra.git"})
	createBody := capture("create repo1", createResp)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create repo1: %d %s", createResp.StatusCode, createBody)
	}
	var created markedRepoBody
	if err := json.Unmarshal([]byte(createBody), &created); err != nil {
		t.Fatal(err)
	}
	repoID := created.Repo.ID

	// Same name as an existing org-A repo: an ordinary, same-organization
	// 409 — the shape closest to decision 7's message, which is exactly why
	// it belongs in this test.
	capture("create duplicate name", postJSON(t, operatorClient, base, map[string]any{
		"name": "repo1", "url": "https://git.example.com/a/infra-2.git",
	}))

	// Same name as ORG B's own repository: org A is not even aware of a
	// conflict, because there isn't one — tenancy isolation, not decision
	// 7, is what makes this succeed.
	sameNameAsB := postJSON(t, operatorClient, base, map[string]any{
		"name": "shared-candidate", "url": "https://git.example.com/a/also-shared.git",
	})
	capture("create same name as org B's repo", sameNameAsB)
	if sameNameAsB.StatusCode != http.StatusCreated {
		t.Fatalf("org A creating a repo named like org B's own: %d, want 201 (no cross-org collision)", sameNameAsB.StatusCode)
	}

	capture("get repo1", requestJSON(t, operatorClient, http.MethodGet, base+"/"+repoID, nil))
	capture("update repo1", requestJSON(t, operatorClient, http.MethodPut, base+"/"+repoID, map[string]any{
		"name": "repo1-renamed", "url": "https://git.example.com/a/infra-renamed.git", "branch": "main",
	}))

	capture("sync repo1", postJSON(t, operatorClient, base+"/"+repoID+"/sync", nil))
	capture("clear host key", requestJSON(t, operatorClient, http.MethodDelete, base+"/"+repoID+"/host-key", nil))
	capture("delete repo1", requestJSON(t, operatorClient, http.MethodDelete, base+"/"+repoID, nil))

	// A non-admin member of org A reaching the instance-admin-only
	// definition routes — covered for 403 already
	// (TestRepoDefinitionRoutesRequireInstanceAdmin); captured here too so
	// this test's own body-text assertion covers them as well.
	adminBase := s.HTTP.URL + "/api/v1/admin/repos/definitions"
	capture("admin list as org A member", requestJSON(t, operatorClient, http.MethodGet, adminBase, nil))
	capture("admin create as org A member", postJSON(t, operatorClient, adminBase, map[string]any{
		"name": "shared-candidate", "url": "https://git.example.com/a/infra.git",
	}))

	// orgB.String() is the canonical, lowercase, hyphenated form — the only
	// shape google/uuid ever renders it in, by fmt's %s (what
	// *shared.InUseError.Error() and *shared.NameCollisionError.Error() use)
	// or by JSON encoding (uuid.UUID has no custom MarshalJSON, so it falls
	// back to the same MarshalText). There is no second encoding to also
	// forbid.
	forbidden := []string{orgBIdentity, orgB.String()}
	for i, body := range bodies {
		for _, identity := range forbidden {
			if strings.Contains(body, identity) {
				t.Fatalf("response #%d body contains organization B's identity %q: %s", i, identity, body)
			}
		}
	}
}
