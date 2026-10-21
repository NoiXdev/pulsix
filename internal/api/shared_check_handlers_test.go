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
	// Review finding: naming the organization is not enough for check
	// definitions specifically — its own collision may sit at a binding the
	// organization configured, invisible on that organization's own
	// org-wide list. The scope must reach the instance admin too, not just
	// the organization.
	if !strings.Contains(body, "scope") {
		t.Fatalf("collision response body = %q, want it to name the scope the collision is at", body)
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

// TestDeletingABoundMonitoringGroupThroughTheRealAPIPreservesTheProjection
// is migration 00063's own fix, proven through the REAL HTTP stack rather
// than only against raw SQL (internal/db/schema_test.go's own
// TestDeletingABoundGroupPreservesASharedProjectionAtOrgScope proves the
// trigger mechanism itself) — a review's Critical finding: deleting a
// monitoring group a customer has rebound a shared projection to CASCADES
// (migration 00043) and never reaches checks.Service.DeleteDefinition's own
// origin == "shared" refusal at all, because the delete happens against
// monitoring_groups, not check_definitions. This drives the exact customer
// actions end to end — define a shared check, rebind the org's own
// projection to a real monitoring group through PUT, delete that group
// through the real DELETE /orgs/{org}/monitoring-groups/{id} route — and
// asserts the projection SURVIVES, rebound to org scope, not merely that
// the group's own delete returned 200.
func TestDeletingABoundMonitoringGroupThroughTheRealAPIPreservesTheProjection(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("check-cascade-group-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("check-cascade-group-owner@example.test", "correct horse battery")
	orgSlug := "check-cascade-group-org"
	s.SeedOrg(orgSlug, owner)
	ownerClient := s.LoginAs(owner)

	resp := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/checks/definitions", map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
	})
	var created checkDefinitionDefDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/checks", nil)
	var checks markedCheckListBody
	decodeInto(t, resp, &checks)
	projected, ok := findProjectedCheck(checks, defID)
	if !ok {
		t.Fatalf("org checks = %+v, want a projection of %s", checks.Checks, defID)
	}

	groupResp := postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/monitoring-groups", map[string]any{
		"name": "web-fleet", "selector": "role=web", "priority": 0,
	})
	if groupResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitoring group: %d %s", groupResp.StatusCode, mustReadBody(t, groupResp))
	}
	var group struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, groupResp)), &group); err != nil {
		t.Fatal(err)
	}

	// The customer rebinds their own projection to the group — exactly the
	// write this task's own feature added, exercised here through the real
	// route rather than a direct SQL UPDATE.
	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/checks/"+projected.ID, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
		"binding_kind": "group", "group_id": group.Group.ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebind the projection to the group: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var rebound markedCheckBody
	decodeInto(t, resp, &rebound)
	if rebound.Check.BindingKind != "group" {
		t.Fatalf("rebound.Check.BindingKind = %q, want group", rebound.Check.BindingKind)
	}

	// The customer now deletes the group through the real route — their own
	// action, on their own fleet, which must succeed.
	resp = requestJSON(t, ownerClient, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/monitoring-groups/"+group.Group.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete the monitoring group: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The projection must still exist, rebound to org scope — not merely
	// that the group's own delete returned 200.
	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/checks/"+projected.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get the projection after its group was deleted: %d %s, want 200 (it must survive)", resp.StatusCode, mustReadBody(t, resp))
	}
	var afterDelete markedCheckBody
	decodeInto(t, resp, &afterDelete)
	if afterDelete.Check.BindingKind != "org" || afterDelete.Check.SharedDefinitionID != defID {
		t.Fatalf("projection after its group was deleted = %+v, want binding_kind=org and the SAME shared_definition_id %s", afterDelete.Check, defID)
	}
}

// TestDeletingABoundHostThroughTheRealAPIPreservesTheProjection is
// TestDeletingABoundMonitoringGroupThroughTheRealAPIPreservesTheProjection's
// own test for a host binding — the second call site the review's Critical
// named (hosts.Service.DeleteHost), proven through the real
// DELETE /orgs/{org}/hosts/{id} route.
func TestDeletingABoundHostThroughTheRealAPIPreservesTheProjection(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("check-cascade-host-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("check-cascade-host-owner@example.test", "correct horse battery")
	orgSlug := "check-cascade-host-org"
	s.SeedOrg(orgSlug, owner)
	ownerClient := s.LoginAs(owner)

	resp := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/checks/definitions", map[string]any{
		"key": "cpu-watch", "plugin": "cpu", "interval_seconds": 60,
	})
	var created checkDefinitionDefDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/checks", nil)
	var checks markedCheckListBody
	decodeInto(t, resp, &checks)
	projected, ok := findProjectedCheck(checks, defID)
	if !ok {
		t.Fatalf("org checks = %+v, want a projection of %s", checks.Checks, defID)
	}

	zonesResp := requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/zones", nil)
	var zones struct {
		Zones []struct {
			ID string `json:"id"`
		} `json:"zones"`
	}
	decodeInto(t, zonesResp, &zones)
	if len(zones.Zones) == 0 {
		t.Fatal("expected a default zone")
	}
	hostResp := postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/hosts", map[string]any{
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

	resp = requestJSON(t, ownerClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/checks/"+projected.ID, map[string]any{
		"key": "cpu-watch", "plugin": "cpu", "interval_seconds": 60,
		"binding_kind": "host", "host_id": host.Host.ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebind the projection to the host: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, ownerClient, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/hosts/"+host.Host.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete the host: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/checks/"+projected.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get the projection after its host was deleted: %d %s, want 200 (it must survive)", resp.StatusCode, mustReadBody(t, resp))
	}
	var afterDelete markedCheckBody
	decodeInto(t, resp, &afterDelete)
	if afterDelete.Check.BindingKind != "org" || afterDelete.Check.SharedDefinitionID != defID {
		t.Fatalf("projection after its host was deleted = %+v, want binding_kind=org and the SAME shared_definition_id %s", afterDelete.Check, defID)
	}
}

// TestOrgCreationProjectsAnExistingSharedCheckDefinition is a whole-branch
// review's own finding: ProjectAllCheckDefinitionsInto has exactly ONE
// production call site (internal/app/app.go's own ChainOrgCreatedHooks),
// and no test outside internal/shared's own unit tests ever drove it
// through a real organization-creation route. Deleting that one argument
// from app.go would leave the whole suite green while every organization
// created afterwards silently received no shared check definitions at all
// — decision 4 broken for one of the three kinds, with nothing to catch
// it. This proves the real POST /orgs route, through apitest's own hook
// wiring (which must match app.go's, not drift from it), actually projects
// a check definition created BEFORE the organization existed — decision
// 4's "creating an organization projects every existing definition into
// it," for this kind specifically, exercised for real rather than assumed
// from the two OTHER kinds already having their own such test.
func TestOrgCreationProjectsAnExistingSharedCheckDefinition(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("check-proj-on-create-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	resp := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/checks/definitions", map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create shared check definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created checkDefinitionDefDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID

	owner := s.SeedUser("check-proj-on-create-owner@example.test", "correct horse battery")
	ownerClient := s.LoginAs(owner)
	resp = postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs", map[string]any{
		"slug": "check-proj-on-create-org", "name": "Check Proj On Create Org",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create org: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/check-proj-on-create-org/checks", nil)
	var checks markedCheckListBody
	decodeInto(t, resp, &checks)
	if _, ok := findProjectedCheck(checks, defID); !ok {
		t.Fatalf("checks in the brand-new organization = %+v, want a projection of the pre-existing shared definition %s", checks.Checks, defID)
	}
}

// TestRebindingACustomCheckOntoASharedProjectionsOrgSlotIsRefused is the
// whole-branch review's own finding: migration 00063's trigger rebinds a
// shared projection to ('org', nil, nil) when the group or host it was
// bound to is deleted, and checks.Service.refuseKeyHeldBySharedProjection
// keeps that slot free — but the old call site only ran the guard when the
// UPDATE changed the key, so a customer's own check could take the slot by
// REBINDING to org scope with its key left alone. This drives the full
// five-step sequence through the real routes and the real migration 00063
// trigger — nothing short of the real database can prove step 5, since
// that is where the trigger's own UPDATE either lands cleanly or raises
// 23505:
//
//  1. the customer's OWN check "disk-data", bound to a host — created
//     BEFORE the shared definition exists, so the create-time guard
//     (which blocks a new row at ANY scope once a shared projection holds
//     the key) never saw it.
//  2. the provider defines the shared "disk-data" check; the preflight
//     collision check only looks at org-scope rows, so the customer's
//     host-bound row is invisible to it and the projection lands at org
//     scope with no reported collision.
//  3. the customer rebinds the SHARED projection to a monitoring group,
//     freeing the ('org', nil, nil, "disk-data") slot.
//  4. the customer rebinds their OWN check from the host to org scope,
//     same key — must now be REFUSED (400), where before this fix it
//     silently took the slot.
//  5. the customer deletes the group — before the fix this aborted with a
//     500 (mapChecksErr has no case for the trigger's own 23505, and
//     DeleteGroup never even reaches it under the old, working sequence);
//     with step 4 correctly refused, the slot is never taken and this
//     delete succeeds, landing the shared projection back at org scope.
func TestRebindingACustomCheckOntoASharedProjectionsOrgSlotIsRefused(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("check-rebind-slot-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	adminClient := s.LoginAs(admin)

	owner := s.SeedUser("check-rebind-slot-owner@example.test", "correct horse battery")
	orgSlug := "check-rebind-slot-org"
	s.SeedOrg(orgSlug, owner)
	ownerClient := s.LoginAs(owner)

	// Step 1: the customer's own check, host-bound, BEFORE any shared
	// definition of the same key exists.
	zonesResp := requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/zones", nil)
	var zones struct {
		Zones []struct {
			ID string `json:"id"`
		} `json:"zones"`
	}
	decodeInto(t, zonesResp, &zones)
	if len(zones.Zones) == 0 {
		t.Fatal("expected a default zone")
	}
	hostResp := postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/hosts", map[string]any{
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

	ownChecksBase := s.HTTP.URL + "/api/v1/orgs/" + orgSlug + "/checks"
	customResp := postJSON(t, ownerClient, ownChecksBase, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
		"binding_kind": "host", "host_id": host.Host.ID,
	})
	if customResp.StatusCode != http.StatusCreated {
		t.Fatalf("seed the customer's own host-bound check: %d %s", customResp.StatusCode, mustReadBody(t, customResp))
	}
	var custom markedCheckBody
	if err := json.Unmarshal([]byte(mustReadBody(t, customResp)), &custom); err != nil {
		t.Fatal(err)
	}

	// Step 2: the provider's shared definition, same key — the preflight
	// never sees the customer's host-bound row, so this succeeds with no
	// reported collision.
	defResp := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/checks/definitions", map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
	})
	if defResp.StatusCode != http.StatusCreated {
		t.Fatalf("create shared check definition: %d %s", defResp.StatusCode, mustReadBody(t, defResp))
	}
	var created checkDefinitionDefDetailBody
	if err := json.Unmarshal([]byte(mustReadBody(t, defResp)), &created); err != nil {
		t.Fatal(err)
	}
	defID := created.Definition.ID

	listResp := requestJSON(t, ownerClient, http.MethodGet, ownChecksBase, nil)
	var checks markedCheckListBody
	decodeInto(t, listResp, &checks)
	projected, ok := findProjectedCheck(checks, defID)
	if !ok {
		t.Fatalf("org checks = %+v, want a projection of %s", checks.Checks, defID)
	}
	if projected.BindingKind != "org" {
		t.Fatalf("fresh projection binding_kind = %q, want org", projected.BindingKind)
	}

	// Step 3: the customer rebinds the SHARED projection to a monitoring
	// group, freeing the org-scope slot its key needs back.
	groupResp := postJSON(t, ownerClient, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/monitoring-groups", map[string]any{
		"name": "web-fleet", "selector": "role=web", "priority": 0,
	})
	if groupResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitoring group: %d %s", groupResp.StatusCode, mustReadBody(t, groupResp))
	}
	var group struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, groupResp)), &group); err != nil {
		t.Fatal(err)
	}
	rebindSharedResp := requestJSON(t, ownerClient, http.MethodPut, ownChecksBase+"/"+projected.ID, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300,
		"binding_kind": "group", "group_id": group.Group.ID,
	})
	if rebindSharedResp.StatusCode != http.StatusOK {
		t.Fatalf("rebind the shared projection to the group: %d %s", rebindSharedResp.StatusCode, mustReadBody(t, rebindSharedResp))
	}

	// Step 4: rebinding the CUSTOMER'S OWN check to org scope, key
	// unchanged, must now be refused — this is the exact gap the fix
	// closes. t.Errorf rather than t.Fatalf here and below, deliberately:
	// this test is also what proves the SYMPTOM the bug report opens
	// with — without the fix this assertion fails, but so does step 5's
	// below it (the trigger's own 23505 surfacing as a 500 through
	// mapChecksErr), and a Fatalf here would stop the test before ever
	// reaching that second, customer-visible failure.
	rebindCustomResp := requestJSON(t, ownerClient, http.MethodPut, ownChecksBase+"/"+custom.Check.ID, map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 300, "binding_kind": "org",
	})
	if rebindCustomResp.StatusCode != http.StatusBadRequest {
		t.Errorf("rebinding the customer's own check onto the shared key's org slot: %d %s, want 400", rebindCustomResp.StatusCode, mustReadBody(t, rebindCustomResp))
	}

	// The customer's own check must still be exactly where it was — a
	// refused update never partially writes.
	getCustomResp := requestJSON(t, ownerClient, http.MethodGet, ownChecksBase+"/"+custom.Check.ID, nil)
	var afterRefusal markedCheckBody
	decodeInto(t, getCustomResp, &afterRefusal)
	if afterRefusal.Check.BindingKind != "host" {
		t.Errorf("customer's own check after a refused rebind = %+v, want it still bound to the host", afterRefusal.Check)
	}

	// Step 5: deleting the group must now succeed — the slot the trigger
	// needs was never taken. Without the fix, step 4 above silently let the
	// customer's own check take the org-scope slot, and this delete is
	// where that damage actually surfaces: the trigger's UPDATE collides,
	// 23505 aborts DELETE FROM monitoring_groups, and mapChecksErr (no case
	// for a unique violation) turns it into a bare 500 — the customer can
	// no longer remove their own group, with nothing naming a cause.
	deleteGroupResp := requestJSON(t, ownerClient, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/"+orgSlug+"/monitoring-groups/"+group.Group.ID, nil)
	if deleteGroupResp.StatusCode != http.StatusOK {
		t.Fatalf("delete the monitoring group: %d %s, want 200 (the fix must let this succeed)", deleteGroupResp.StatusCode, mustReadBody(t, deleteGroupResp))
	}

	// The shared projection must have survived, rebound back to org scope
	// by the trigger.
	getProjectedResp := requestJSON(t, ownerClient, http.MethodGet, ownChecksBase+"/"+projected.ID, nil)
	if getProjectedResp.StatusCode != http.StatusOK {
		t.Fatalf("get the shared projection after its group was deleted: %d %s, want 200", getProjectedResp.StatusCode, mustReadBody(t, getProjectedResp))
	}
	var afterGroupDelete markedCheckBody
	decodeInto(t, getProjectedResp, &afterGroupDelete)
	if afterGroupDelete.Check.BindingKind != "org" || afterGroupDelete.Check.SharedDefinitionID != defID {
		t.Fatalf("shared projection after its group was deleted = %+v, want binding_kind=org and the SAME shared_definition_id %s", afterGroupDelete.Check, defID)
	}
}
