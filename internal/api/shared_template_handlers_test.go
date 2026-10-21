package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// templateDefinitionDTO decodes enough of the installation-owned half of a
// shared job template (shared_handlers.go's own templateDefinitionDTO) for
// these tests: name, the repository it resolves against, its path within
// that repository, and limit.
type templateDefinitionDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SharedRepoID string `json:"shared_repo_id"`
	PlaybookPath string `json:"playbook_path"`
	Limit        string `json:"limit"`
}

type templateDefinitionDetailBody struct {
	Definition templateDefinitionDTO `json:"definition"`
}

type templateDefinitionListBody struct {
	Definitions []templateDefinitionDTO `json:"definitions"`
}

// markedTemplateBody decodes just enough of GET/POST .../job-templates' own
// templateDTO (template_handlers.go) to see Task 6's marker:
// SharedTemplateID, set only on a projection, and PlaybookID, "" while
// unresolved (toTemplateDTO's own comment).
type markedTemplateBody struct {
	Template struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		PlaybookID       string `json:"playbook_id"`
		Limit            string `json:"limit"`
		SharedTemplateID string `json:"shared_template_id"`
	} `json:"template"`
}

type markedTemplateListBody struct {
	Templates []struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		PlaybookID       string `json:"playbook_id"`
		Limit            string `json:"limit"`
		SharedTemplateID string `json:"shared_template_id"`
	} `json:"templates"`
}

// seedRepoDefinition creates a shared repository definition through the
// real instance-admin route — the raw material every job template
// definition in these tests references (shared_job_templates.shared_repo_id
// is NOT NULL, migration 00061's own reason).
func seedRepoDefinition(t *testing.T, adminClient *http.Client, s *apitest.Server, name string) string {
	t.Helper()
	resp := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/repos/definitions", map[string]any{
		"name": name, "url": "https://git.example.com/" + name + ".git", "branch": "main",
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed repo definition %q: %d %s", name, resp.StatusCode, body)
	}
	var created repoDefinitionDetailBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	return created.Definition.ID
}

// TestTemplateDefinitionLifecycleAndProjectionMarker is
// TestRepoDefinitionLifecycleAndProjectionMarker's own test for job
// templates (Task 6): an instance admin's CRUD over a job template
// definition, its immediate projection into an existing organization (spec
// decision 4), the shared_template_id marker on that organization's own
// templateDTO, and that the projection refuses an organization's own
// attempt to change a shared field or delete it (decisions 2 and 6).
func TestTemplateDefinitionLifecycleAndProjectionMarker(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("tpl-def-lifecycle-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("tpl-def-lifecycle-owner@example.test", "correct horse battery")
	s.SeedOrg("tpl-def-lifecycle-org", owner)
	ownerClient := s.LoginAs(owner)

	repoDefID := seedRepoDefinition(t, adminClient, s, "infra-for-tpl-lifecycle")

	defsBase := s.HTTP.URL + "/api/v1/admin/templates/definitions"
	resp := postJSON(t, adminClient, defsBase, map[string]any{
		"name": "deploy-site", "shared_repo_id": repoDefID, "playbook_path": "site.yml", "limit": "web*",
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create template definition: %d %s", resp.StatusCode, body)
	}
	var created templateDefinitionDetailBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID
	if defID == "" || created.Definition.Limit != "web*" {
		t.Fatalf("created definition = %+v, want a non-empty id and limit web*", created.Definition)
	}

	// It appears in the list and GET /{id}.
	resp = requestJSON(t, adminClient, http.MethodGet, defsBase, nil)
	var list templateDefinitionListBody
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
	// decision 4), marked with shared_template_id. playbook_id is "" —
	// nothing in this organization has synced "site.yml" from the
	// repository yet (toTemplateDTO's own comment on the unresolved case).
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tpl-def-lifecycle-org/job-templates", nil)
	var templates markedTemplateListBody
	decodeInto(t, resp, &templates)
	if len(templates.Templates) != 1 {
		t.Fatalf("org templates = %+v, want exactly one projection", templates.Templates)
	}
	projected := templates.Templates[0]
	if projected.SharedTemplateID != defID {
		t.Fatalf("projected.SharedTemplateID = %q, want %s", projected.SharedTemplateID, defID)
	}
	if projected.Name != "deploy-site" || projected.Limit != "web*" {
		t.Fatalf("projected = %+v, want name deploy-site, limit web*", projected)
	}
	if projected.PlaybookID != "" {
		t.Fatalf("projected.PlaybookID = %q, want \"\" (unresolved — no matching playbook synced)", projected.PlaybookID)
	}

	// Editing the definition propagates (spec decision 2) — the
	// organization's own limit changes with no action on its part.
	resp = requestJSON(t, adminClient, http.MethodPut, defsBase+"/"+defID, map[string]any{
		"name": "deploy-site", "shared_repo_id": repoDefID, "playbook_path": "site.yml", "limit": "db*",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tpl-def-lifecycle-org/job-templates/"+projected.ID, nil)
	var afterEdit markedTemplateBody
	decodeInto(t, resp, &afterEdit)
	if afterEdit.Template.Limit != "db*" {
		t.Fatalf("org's own template limit after admin edit = %q, want db* (propagation)", afterEdit.Template.Limit)
	}

	// The organization cannot change a shared field itself (decision 2) ...
	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tpl-def-lifecycle-org/job-templates/"+projected.ID, map[string]any{
		"name": "deploy-site", "playbook_id": "", "limit": "attacker-limit",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("org changing a shared field: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	// ... nor delete the projection itself (decision 6).
	resp = requestJSON(t, ownerClient, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/tpl-def-lifecycle-org/job-templates/"+projected.ID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("org deleting its own projection: %d %s, want 403", resp.StatusCode, mustReadBody(t, resp))
	}

	// Deleting the definition takes the projection with it (spec decision
	// 5: nothing references it yet, so this succeeds outright).
	resp = requestJSON(t, adminClient, http.MethodDelete, defsBase+"/"+defID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tpl-def-lifecycle-org/job-templates", nil)
	var afterDelete markedTemplateListBody
	decodeInto(t, resp, &afterDelete)
	if len(afterDelete.Templates) != 0 {
		t.Fatalf("org templates after definition delete = %+v, want none left", afterDelete.Templates)
	}
}

// TestTemplateDefinitionRoutesRequireInstanceAdmin is
// TestRepoDefinitionRoutesRequireInstanceAdmin's own test for job
// templates: a plain member of some organization is refused on every one
// of the five verbs, individually.
func TestTemplateDefinitionRoutesRequireInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	member := s.SeedUser("tpl-def-admin-gate-member@example.test", "correct horse battery")
	s.SeedOrg("tpl-def-admin-gate-org", member)
	c := s.LoginAs(member)
	base := s.HTTP.URL + "/api/v1/admin/templates/definitions"
	someID := "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		name   string
		method string
		url    string
		body   any
	}{
		{"list", http.MethodGet, base, nil},
		{"create", http.MethodPost, base, map[string]any{"name": "x", "shared_repo_id": someID, "playbook_path": "x.yml"}},
		{"get", http.MethodGet, base + "/" + someID, nil},
		{"update", http.MethodPut, base + "/" + someID, map[string]any{"name": "x", "shared_repo_id": someID, "playbook_path": "x.yml"}},
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

// TestTemplateDefinitionCollisionNamesOrganizationToInstanceAdmin is
// TestRepoDefinitionCollisionNamesOrganizationToInstanceAdmin's own test for
// job templates: spec decision 7's collision message must name the
// organization, to an instance admin.
func TestTemplateDefinitionCollisionNamesOrganizationToInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("tpl-def-collision-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	victimOwner := s.SeedUser("tpl-def-collision-victim-owner@example.test", "correct horse battery")
	s.SeedOrg("tpl-collision-victim-org", victimOwner)
	victimClient := s.LoginAs(victimOwner)

	// The victim organization needs a real playbook to create its own,
	// ordinary template named "deploy-site" — nothing to do with any
	// definition.
	pbResp := postJSON(t, victimClient, s.HTTP.URL+"/api/v1/orgs/tpl-collision-victim-org/playbooks", map[string]any{
		"inline": "- name: deploy\n  hosts: all\n  tasks: []\n",
	})
	pbBody := mustReadBody(t, pbResp)
	if pbResp.StatusCode != http.StatusCreated {
		t.Fatalf("seed victim org's own playbook: %d %s", pbResp.StatusCode, pbBody)
	}
	var pb struct {
		Playbook struct {
			ID string `json:"id"`
		} `json:"playbook"`
	}
	if err := json.Unmarshal([]byte(pbBody), &pb); err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, victimClient, s.HTTP.URL+"/api/v1/orgs/tpl-collision-victim-org/job-templates", map[string]any{
		"name": "deploy-site", "playbook_id": pb.Playbook.ID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed victim org's own template: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	repoDefID := seedRepoDefinition(t, adminClient, s, "infra-for-tpl-collision")
	// The instance admin now defines a shared template with that same name
	// — spec decision 7: refused, naming the organization.
	resp = postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/templates/definitions", map[string]any{
		"name": "deploy-site", "shared_repo_id": repoDefID, "playbook_path": "site.yml",
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("create colliding definition: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(body, "tpl-collision-victim-org") {
		t.Fatalf("collision response body = %q, want it to name organization tpl-collision-victim-org", body)
	}
}

// TestTemplateDefinitionInUseNamesOrganizationToInstanceAdmin is
// TestRepoDefinitionInUseNamesOrganizationToInstanceAdmin's own test for job
// templates: spec decision 5's delete refusal must name the organization,
// to an instance admin — a schedule this time, not a job trigger, the
// other dependant the brief calls out.
func TestTemplateDefinitionInUseNamesOrganizationToInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("tpl-def-inuse-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("tpl-def-inuse-owner@example.test", "correct horse battery")
	s.SeedOrg("tpl-inuse-victim-org", owner)
	ownerClient := s.LoginAs(owner)

	repoDefID := seedRepoDefinition(t, adminClient, s, "infra-for-tpl-inuse")
	defsBase := s.HTTP.URL + "/api/v1/admin/templates/definitions"
	resp := postJSON(t, adminClient, defsBase, map[string]any{
		"name": "triggered-template", "shared_repo_id": repoDefID, "playbook_path": "site.yml",
	})
	var created templateDefinitionDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tpl-inuse-victim-org/job-templates", nil)
	var templates markedTemplateListBody
	decodeInto(t, resp, &templates)
	if len(templates.Templates) != 1 {
		t.Fatalf("org templates = %+v, want exactly one projection", templates.Templates)
	}
	projectedID := templates.Templates[0].ID

	// A schedule on the projection is customer configuration (spec decision
	// 5's own wording: "a schedule, a trigger or a job"), created through
	// the real org-scoped route exactly as an organization would.
	resp = postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/tpl-inuse-victim-org/schedules", map[string]any{
		"template_id": projectedID, "name": "nightly", "mode": "cron", "cron": "0 2 * * *", "timezone": "UTC",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed schedule: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, adminClient, http.MethodDelete, defsBase+"/"+defID, nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete definition in use: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(body, "tpl-inuse-victim-org") {
		t.Fatalf("in-use response body = %q, want it to name organization tpl-inuse-victim-org", body)
	}
}

// TestOrgScopedTemplateRoutesNeverLeakAnotherOrgsIdentity is
// TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity's own test for job
// templates: a member of organization A provokes every organization-scoped
// job-template path reachable from org A, and none of the response bodies
// may ever mention organization B's slug, name or raw id.
func TestOrgScopedTemplateRoutesNeverLeakAnotherOrgsIdentity(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("tpl-leak-org-a-owner@example.test", "correct horse battery")
	orgASlug := "tpl-leak-org-a"
	orgA := s.SeedOrg(orgASlug, ownerA)
	operatorA := s.SeedUser("tpl-leak-org-a-operator@example.test", "correct horse battery")
	s.AddMember(orgA, operatorA, tenancy.RoleOperator)
	operatorClient := s.LoginAs(operatorA)

	ownerB := s.SeedUser("tpl-leak-org-b-owner@example.test", "correct horse battery")
	const orgBIdentity = "tpl-leak-secret-org-b"
	orgB := s.SeedOrg(orgBIdentity, ownerB)
	bClient := s.LoginAs(ownerB)

	// Org B needs a playbook before it can own a template of the same name
	// org A is about to create — a real, same-named row elsewhere, so a
	// leak would have something concrete to leak.
	pbResp := postJSON(t, bClient, s.HTTP.URL+"/api/v1/orgs/"+orgBIdentity+"/playbooks", map[string]any{
		"inline": "- name: deploy\n  hosts: all\n  tasks: []\n",
	})
	if pbResp.StatusCode != http.StatusCreated {
		t.Fatalf("seed org B's playbook: %d %s", pbResp.StatusCode, mustReadBody(t, pbResp))
	}
	var pbB struct {
		Playbook struct {
			ID string `json:"id"`
		} `json:"playbook"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, pbResp)), &pbB); err != nil {
		t.Fatal(err)
	}
	if resp := postJSON(t, bClient, s.HTTP.URL+"/api/v1/orgs/"+orgBIdentity+"/job-templates", map[string]any{
		"name": "shared-candidate", "playbook_id": pbB.Playbook.ID,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed org B's template: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// Org A needs its own playbook to create ordinary templates too.
	pbRespA := postJSON(t, operatorClient, s.HTTP.URL+"/api/v1/orgs/"+orgASlug+"/playbooks", map[string]any{
		"inline": "- name: deploy\n  hosts: all\n  tasks: []\n",
	})
	if pbRespA.StatusCode != http.StatusCreated {
		t.Fatalf("seed org A's playbook: %d %s", pbRespA.StatusCode, mustReadBody(t, pbRespA))
	}
	var pbA struct {
		Playbook struct {
			ID string `json:"id"`
		} `json:"playbook"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, pbRespA)), &pbA); err != nil {
		t.Fatal(err)
	}

	base := s.HTTP.URL + "/api/v1/orgs/" + orgASlug + "/job-templates"
	var bodies []string
	capture := func(label string, resp *http.Response) string {
		t.Helper()
		body := mustReadBody(t, resp)
		bodies = append(bodies, body)
		t.Logf("%s: %d %s", label, resp.StatusCode, body)
		return body
	}

	capture("list (empty)", requestJSON(t, operatorClient, http.MethodGet, base, nil))

	createResp := postJSON(t, operatorClient, base, map[string]any{"name": "template1", "playbook_id": pbA.Playbook.ID})
	createBody := capture("create template1", createResp)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create template1: %d %s", createResp.StatusCode, createBody)
	}
	var created markedTemplateBody
	if err := json.Unmarshal([]byte(createBody), &created); err != nil {
		t.Fatal(err)
	}
	templateID := created.Template.ID

	// Same name as an existing org-A template: an ordinary, same-organization
	// 409.
	capture("create duplicate name", postJSON(t, operatorClient, base, map[string]any{
		"name": "template1", "playbook_id": pbA.Playbook.ID,
	}))

	// Same name as ORG B's own template: org A is not even aware of a
	// conflict, because there isn't one — tenancy isolation, not decision
	// 7, is what makes this succeed.
	sameNameAsB := postJSON(t, operatorClient, base, map[string]any{
		"name": "shared-candidate", "playbook_id": pbA.Playbook.ID,
	})
	capture("create same name as org B's template", sameNameAsB)
	if sameNameAsB.StatusCode != http.StatusCreated {
		t.Fatalf("org A creating a template named like org B's own: %d, want 201 (no cross-org collision)", sameNameAsB.StatusCode)
	}

	capture("get template1", requestJSON(t, operatorClient, http.MethodGet, base+"/"+templateID, nil))
	capture("update template1", requestJSON(t, operatorClient, http.MethodPut, base+"/"+templateID, map[string]any{
		"name": "template1-renamed", "playbook_id": pbA.Playbook.ID,
	}))
	capture("delete template1", requestJSON(t, operatorClient, http.MethodDelete, base+"/"+templateID, nil))

	// A non-admin member of org A reaching the instance-admin-only
	// definition routes.
	adminBase := s.HTTP.URL + "/api/v1/admin/templates/definitions"
	capture("admin list as org A member", requestJSON(t, operatorClient, http.MethodGet, adminBase, nil))
	capture("admin create as org A member", postJSON(t, operatorClient, adminBase, map[string]any{
		"name": "shared-candidate", "shared_repo_id": "00000000-0000-0000-0000-000000000000", "playbook_path": "x.yml",
	}))

	forbidden := []string{orgBIdentity, orgB.String()}
	for i, body := range bodies {
		for _, identity := range forbidden {
			if strings.Contains(body, identity) {
				t.Fatalf("response #%d body contains organization B's identity %q: %s", i, identity, body)
			}
		}
	}
}

// repoListBody decodes enough of GET .../repos to find an organization's
// own projection of a shared repository definition by its shared_repo_id
// marker (repo_handlers.go's own repoDTO).
type repoListBody struct {
	Repos []struct {
		ID           string `json:"id"`
		SharedRepoID string `json:"shared_repo_id"`
	} `json:"repos"`
}

// TestRunningAProjectedTemplateBeforeAndAfterItsPlaybookSyncs is the
// end-to-end proof for both review findings on Task 6's first draft
// (task-6-report.md): a shared template definition is the normal order
// this feature exists for — configure centrally, THEN a customer syncs the
// matching playbook (spec decision 4) — and until it does, attempting to
// run the resulting projection must fail with a diagnosable 422, not a 404
// indistinguishable from a deleted template (finding 2); once the
// organization registers the matching playbook, the SAME template, same
// id, must now actually run — proving the organization-side resolution
// trigger (finding 1, playbooks.Service.CreateRepo calling into
// shared.Service.ResolveTemplatePlaybooks) is wired all the way through
// the real HTTP stack, not only unit-tested in isolation.
func TestRunningAProjectedTemplateBeforeAndAfterItsPlaybookSyncs(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("tpl-run-sync-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("tpl-run-sync-owner@example.test", "correct horse battery")
	orgSlug := "tpl-run-sync-org"
	orgID := s.SeedOrg(orgSlug, owner)
	ownerClient := s.LoginAs(owner)
	scoped := db.WithOrgScope(context.Background(), orgID)

	// A managed host and its own ssh login — the run's scope target and
	// the credential a claim needs to connect, exactly job_handlers_test.go's
	// own newRunFixture setup, trimmed to one host.
	zones, err := s.Deps.Hosts.ListZones(scoped, orgID)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	host, err := s.Deps.Hosts.CreateHost(scoped, orgID, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	sshCred, err := s.Deps.Credentials.Create(scoped, orgID, credentials.CreateInput{
		Name: "host-login", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deps.SSH.SetHostSSH(scoped, orgID, host.ID, sshconf.HostSSHInput{
		CredentialID: &sshCred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}

	// The shared repository and job template definitions, through the real
	// instance-admin routes — exactly the normal order spec decision 4
	// describes: defined centrally, before this organization has synced
	// anything.
	repoDefID := seedRepoDefinition(t, adminClient, s, "infra-for-run-sync")
	defsBase := s.HTTP.URL + "/api/v1/admin/templates/definitions"
	resp := postJSON(t, adminClient, defsBase, map[string]any{
		"name": "deploy-site", "shared_repo_id": repoDefID, "playbook_path": "site.yml",
	})
	var created templateDefinitionDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &created); err != nil {
		t.Fatal(err)
	}

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/job-templates", nil)
	var tmpls markedTemplateListBody
	decodeInto(t, resp, &tmpls)
	if len(tmpls.Templates) != 1 {
		t.Fatalf("org templates = %+v, want exactly one projection", tmpls.Templates)
	}
	projID := tmpls.Templates[0].ID

	// The organization fills in its own half: scope and the ssh credential
	// (spec decision 2 — these are never the definition's). Needed before a
	// run can target anything at all, and orthogonal to the playbook sync
	// below.
	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/job-templates/"+projID, map[string]any{
		"name": "deploy-site", "playbook_id": "", "limit": "",
		"scope": map[string]any{"host_ids": []string{host.ID.String()}}, "credential_id": sshCred.ID.String(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set the projection's own scope/credential: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// Before any sync: a diagnosable 422, not a 404 indistinguishable from
	// a deleted template (finding 2).
	resp = postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/job-templates/"+projID+"/run", nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("run before sync: %d %s, want 422", resp.StatusCode, body)
	}
	if !strings.Contains(body, "has not been registered") {
		t.Fatalf("run before sync body = %q, want it to name the unresolved playbook, not look like a generic not-found", body)
	}

	// The organization now registers "site.yml" against its OWN projection
	// of the shared repository — playbooks.Service.CreateRepo's real path,
	// the event finding 1's resolver hooks.
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/repos", nil)
	var repoList repoListBody
	decodeInto(t, resp, &repoList)
	var projectedRepoID string
	for _, r := range repoList.Repos {
		if r.SharedRepoID == repoDefID {
			projectedRepoID = r.ID
		}
	}
	if projectedRepoID == "" {
		t.Fatalf("repos = %+v, want the organization's own projection of %s", repoList.Repos, repoDefID)
	}
	resp = postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/playbooks", map[string]any{
		"repo_id": projectedRepoID, "path": "site.yml",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register the playbook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The SAME template, same id, now runs — the resolution trigger ran
	// without the provider ever touching the definition again.
	resp = postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/job-templates/"+projID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run after sync: %d %s, want 202", resp.StatusCode, mustReadBody(t, resp))
	}
}
