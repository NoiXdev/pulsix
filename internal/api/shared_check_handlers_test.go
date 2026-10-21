package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// checkDefinitionDefDTO decodes enough of the installation-owned half of a
// shared check definition (shared_handlers.go's own checkDefinitionDefDTO)
// for these tests: key, plugin and interval. No binding, no enabled flag —
// decision 2 keeps both on the projection for check definitions.
type checkDefinitionDefDTO struct {
	ID              string `json:"id"`
	Key             string `json:"key"`
	Plugin          string `json:"plugin"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type checkDefinitionDefDetailBody struct {
	Definition checkDefinitionDefDTO `json:"definition"`
}

type checkDefinitionDefListBody struct {
	Definitions []checkDefinitionDefDTO `json:"definitions"`
}

// markedCheckBody decodes just enough of GET/POST .../checks' own
// checkDefinitionDTO (check_handlers.go) to see Task 7's marker:
// SharedDefinitionID, set only on a projection, plus the binding fields that
// stay editable regardless of it.
type markedCheckBody struct {
	Check struct {
		ID                 string `json:"id"`
		Key                string `json:"key"`
		Plugin             string `json:"plugin"`
		IntervalSeconds    int    `json:"interval_seconds"`
		Enabled            bool   `json:"enabled"`
		Origin             string `json:"origin"`
		BindingKind        string `json:"binding_kind"`
		SharedDefinitionID string `json:"shared_definition_id"`
	} `json:"check"`
}

type markedCheckListBody struct {
	Checks []struct {
		ID                 string `json:"id"`
		Key                string `json:"key"`
		Plugin             string `json:"plugin"`
		IntervalSeconds    int    `json:"interval_seconds"`
		Enabled            bool   `json:"enabled"`
		Origin             string `json:"origin"`
		BindingKind        string `json:"binding_kind"`
		SharedDefinitionID string `json:"shared_definition_id"`
	} `json:"checks"`
}

// findProjectedCheck returns the one row in list whose SharedDefinitionID
// matches defID — a brand-new organization already has the "Linux base"
// seed set (checks.SeedDefinitions) alongside any projection, so tests
// cannot assume the projection is the only row, or the first one.
func findProjectedCheck(list markedCheckListBody, defID string) (row struct {
	ID                 string `json:"id"`
	Key                string `json:"key"`
	Plugin             string `json:"plugin"`
	IntervalSeconds    int    `json:"interval_seconds"`
	Enabled            bool   `json:"enabled"`
	Origin             string `json:"origin"`
	BindingKind        string `json:"binding_kind"`
	SharedDefinitionID string `json:"shared_definition_id"`
}, ok bool,
) {
	for _, c := range list.Checks {
		if c.SharedDefinitionID == defID {
			return c, true
		}
	}
	return row, false
}

// TestCheckDefinitionLifecycleAndProjectionMarker is
// TestRepoDefinitionLifecycleAndProjectionMarker's own test for check
// definitions (Task 7): an instance admin's CRUD over a check definition,
// its immediate projection into an existing organization (spec decision 4)
// at the default ('org', enabled) binding, the shared_definition_id marker,
// and that the projection refuses an organization's own attempt to change a
// shared field while still allowing it to rebind and disable — the OPPOSITE
// split from a repository or job template.
func TestCheckDefinitionLifecycleAndProjectionMarker(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("check-def-lifecycle-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("check-def-lifecycle-owner@example.test", "correct horse battery")
	s.SeedOrg("check-def-lifecycle-org", owner)
	ownerClient := s.LoginAs(owner)

	defsBase := s.HTTP.URL + "/api/v1/admin/checks/definitions"
	resp := postJSON(t, adminClient, defsBase, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create check definition: %d %s", resp.StatusCode, body)
	}
	var created checkDefinitionDefDetailBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID
	if defID == "" || created.Definition.Key != "disk-data" || created.Definition.IntervalSeconds != 300 {
		t.Fatalf("created definition = %+v, want a non-empty id, key disk-data, interval 300", created.Definition)
	}

	// It appears in the list and GET /{id}.
	resp = requestJSON(t, adminClient, http.MethodGet, defsBase, nil)
	var list checkDefinitionDefListBody
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
	// decision 4), marked with shared_definition_id, at the default org-wide
	// binding and enabled — needing no per-organization data to be complete,
	// unlike a repository's credential or a template's playbook_id.
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/checks", nil)
	var checks markedCheckListBody
	decodeInto(t, resp, &checks)
	projected, ok := findProjectedCheck(checks, defID)
	if !ok {
		t.Fatalf("org checks = %+v, want a projection of %s", checks.Checks, defID)
	}
	if projected.Key != "disk-data" || projected.IntervalSeconds != 300 {
		t.Fatalf("projected = %+v, want key disk-data, interval 300", projected)
	}
	if !projected.Enabled || projected.BindingKind != "org" || projected.Origin != "shared" {
		t.Fatalf("projected = %+v, want enabled=true, binding_kind=org, origin=shared by default", projected)
	}

	// Editing the definition propagates (spec decision 2) — the
	// organization's own interval changes with no action on its part.
	resp = requestJSON(t, adminClient, http.MethodPut, defsBase+"/"+defID, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 600,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/checks/"+projected.ID, nil)
	var afterEdit markedCheckBody
	decodeInto(t, resp, &afterEdit)
	if afterEdit.Check.IntervalSeconds != 600 {
		t.Fatalf("org's own check interval after admin edit = %d, want 600 (propagation)", afterEdit.Check.IntervalSeconds)
	}

	// The organization CANNOT change a shared field itself (decision 2) ...
	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/checks/"+projected.ID, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 15, "binding_kind": "org",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("org changing a shared field: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	// ... but CAN rebind it to a host and disable it (the OPPOSITE split
	// from a repository or job template, where the binding stays the
	// definition's own and is what gets refused).
	zonesResp := requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/zones", nil)
	var zones struct {
		Zones []struct {
			ID string `json:"id"`
		} `json:"zones"`
	}
	decodeInto(t, zonesResp, &zones)
	if len(zones.Zones) == 0 {
		t.Fatal("expected a default zone")
	}
	hostResp := postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/hosts", map[string]any{
		"name": "web-1", "address": "10.0.0.1", "zone_id": zones.Zones[0].ID, "managed": true,
	})
	if hostResp.StatusCode != http.StatusCreated {
		t.Fatalf("seed host: %d %s", hostResp.StatusCode, mustReadBody(t, hostResp))
	}
	var host struct {
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, hostResp)), &host); err != nil {
		t.Fatal(err)
	}
	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/checks/"+projected.ID, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 600, "enabled": false,
		"binding_kind": "host", "host_id": host.Host.ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org rebinding its own projection: %d %s, want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var rebound markedCheckBody
	decodeInto(t, resp, &rebound)
	if rebound.Check.Enabled || rebound.Check.BindingKind != "host" {
		t.Fatalf("rebound = %+v, want enabled=false, binding_kind=host", rebound.Check)
	}

	// ... nor delete the projection itself (decision 6).
	resp = requestJSON(t, ownerClient, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/checks/"+projected.ID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("org deleting its own projection: %d %s, want 403", resp.StatusCode, mustReadBody(t, resp))
	}

	// Deleting the definition takes the projection with it (spec decision
	// 5: nothing references check_definitions at all, so this always
	// succeeds outright — the brief's own first structural claim).
	resp = requestJSON(t, adminClient, http.MethodDelete, defsBase+"/"+defID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/check-def-lifecycle-org/checks", nil)
	var afterDelete markedCheckListBody
	decodeInto(t, resp, &afterDelete)
	if _, ok := findProjectedCheck(afterDelete, defID); ok {
		t.Fatalf("org checks after definition delete = %+v, want the projection gone", afterDelete.Checks)
	}
}

// TestCheckDefinitionRoutesRequireInstanceAdmin is
// TestRepoDefinitionRoutesRequireInstanceAdmin's own test for check
// definitions: a plain member of some organization is refused on every one
// of the five verbs, individually.
func TestCheckDefinitionRoutesRequireInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	member := s.SeedUser("check-def-admin-gate-member@example.test", "correct horse battery")
	s.SeedOrg("check-def-admin-gate-org", member)
	c := s.LoginAs(member)
	base := s.HTTP.URL + "/api/v1/admin/checks/definitions"
	someID := "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		name   string
		method string
		url    string
		body   any
	}{
		{"list", http.MethodGet, base, nil},
		{"create", http.MethodPost, base, map[string]any{"plugin": "cpu", "interval_seconds": 60}},
		{"get", http.MethodGet, base + "/" + someID, nil},
		{"update", http.MethodPut, base + "/" + someID, map[string]any{"plugin": "cpu", "interval_seconds": 60}},
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

// TestCheckDefinitionCollisionNamesOrganizationToInstanceAdmin is
// TestRepoDefinitionCollisionNamesOrganizationToInstanceAdmin's own test for
// check definitions: spec decision 7's collision message must name the
// organization, to an instance admin — the collision landing exactly at the
// default ('org', nil, nil) scope a fresh projection always uses.
func TestCheckDefinitionCollisionNamesOrganizationToInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("check-def-collision-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	victimOwner := s.SeedUser("check-def-collision-victim-owner@example.test", "correct horse battery")
	s.SeedOrg("check-collision-victim-org", victimOwner)
	victimClient := s.LoginAs(victimOwner)

	// The victim organization's own, ordinary check, bound org-wide, with
	// the exact key the provider is about to try.
	resp := postJSON(t, victimClient, s.HTTP.URL+"/api/v1/orgs/check-collision-victim-org/checks", map[string]any{
		"key": "custom-watch", "plugin": "cpu", "interval_seconds": 60, "binding_kind": "org",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed victim org's own check: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/checks/definitions", map[string]any{
		"key": "custom-watch", "plugin": "memory", "interval_seconds": 60,
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("create colliding definition: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(body, "check-collision-victim-org") {
		t.Fatalf("collision response body = %q, want it to name organization check-collision-victim-org", body)
	}
}

// TestOrgScopedCheckRoutesNeverLeakAnotherOrgsIdentity is
// TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity's own test for check
// definitions: a member of organization A provokes every organization-scoped
// check path reachable from org A, and none of the response bodies may ever
// mention organization B's slug, name or raw id.
func TestOrgScopedCheckRoutesNeverLeakAnotherOrgsIdentity(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("check-leak-org-a-owner@example.test", "correct horse battery")
	orgASlug := "check-leak-org-a"
	orgA := s.SeedOrg(orgASlug, ownerA)
	operatorA := s.SeedUser("check-leak-org-a-operator@example.test", "correct horse battery")
	s.AddMember(orgA, operatorA, tenancy.RoleOperator)
	operatorClient := s.LoginAs(operatorA)

	ownerB := s.SeedUser("check-leak-org-b-owner@example.test", "correct horse battery")
	const orgBIdentity = "check-leak-secret-org-b"
	orgB := s.SeedOrg(orgBIdentity, ownerB)
	bClient := s.LoginAs(ownerB)

	// Org B owns a check named like the one org A is about to create — a
	// real, same-key row elsewhere, so a leak would have something concrete
	// to leak.
	if resp := postJSON(t, bClient, s.HTTP.URL+"/api/v1/orgs/"+orgBIdentity+"/checks", map[string]any{
		"key": "shared-candidate", "plugin": "memory", "interval_seconds": 60, "binding_kind": "org",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed org B's check: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	base := s.HTTP.URL + "/api/v1/orgs/" + orgASlug + "/checks"
	var bodies []string
	capture := func(label string, resp *http.Response) string {
		t.Helper()
		body := mustReadBody(t, resp)
		bodies = append(bodies, body)
		t.Logf("%s: %d %s", label, resp.StatusCode, body)
		return body
	}

	capture("list (seed set only)", requestJSON(t, operatorClient, http.MethodGet, base, nil))

	createResp := postJSON(t, operatorClient, base, map[string]any{
		"key": "check1", "plugin": "memory", "interval_seconds": 60, "binding_kind": "org",
	})
	createBody := capture("create check1", createResp)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create check1: %d %s", createResp.StatusCode, createBody)
	}
	var created markedCheckBody
	if err := json.Unmarshal([]byte(createBody), &created); err != nil {
		t.Fatal(err)
	}
	checkID := created.Check.ID

	// Same key+scope as an existing org-A check: an ordinary, same-organization
	// 409.
	capture("create duplicate key", postJSON(t, operatorClient, base, map[string]any{
		"key": "check1", "plugin": "memory", "interval_seconds": 60, "binding_kind": "org",
	}))

	// Same key as ORG B's own check: org A is not even aware of a conflict,
	// because there isn't one — tenancy isolation, not decision 7, is what
	// makes this succeed.
	sameKeyAsB := postJSON(t, operatorClient, base, map[string]any{
		"key": "shared-candidate", "plugin": "memory", "interval_seconds": 60, "binding_kind": "org",
	})
	capture("create same key as org B's check", sameKeyAsB)
	if sameKeyAsB.StatusCode != http.StatusCreated {
		t.Fatalf("org A creating a check named like org B's own: %d, want 201 (no cross-org collision)", sameKeyAsB.StatusCode)
	}

	capture("get check1", requestJSON(t, operatorClient, http.MethodGet, base+"/"+checkID, nil))
	capture("update check1", requestJSON(t, operatorClient, http.MethodPut, base+"/"+checkID, map[string]any{
		"key": "check1-renamed", "plugin": "memory", "interval_seconds": 60, "binding_kind": "org",
	}))
	capture("delete check1", requestJSON(t, operatorClient, http.MethodDelete, base+"/"+checkID, nil))

	// A non-admin member of org A reaching the instance-admin-only
	// definition routes.
	adminBase := s.HTTP.URL + "/api/v1/admin/checks/definitions"
	capture("admin list as org A member", requestJSON(t, operatorClient, http.MethodGet, adminBase, nil))
	capture("admin create as org A member", postJSON(t, operatorClient, adminBase, map[string]any{
		"key": "shared-candidate", "plugin": "memory", "interval_seconds": 60,
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
