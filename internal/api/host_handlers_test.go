package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type zoneDTO struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type hostDTO struct {
	ID         string         `json:"id"`
	ZoneID     string         `json:"zone_id"`
	Name       string         `json:"name"`
	Address    string         `json:"address"`
	Managed    bool           `json:"managed"`
	Monitored  bool           `json:"monitored"`
	Facts      map[string]any `json:"facts"`
	LastSeenAt *string        `json:"last_seen_at,omitempty"`
	CreatedAt  string         `json:"created_at"`
}

func decodeZone(t *testing.T, resp *http.Response) zoneDTO {
	t.Helper()
	var out struct {
		Zone zoneDTO `json:"zone"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Zone
}

func decodeHost(t *testing.T, resp *http.Response) hostDTO {
	t.Helper()
	var out struct {
		Host hostDTO `json:"host"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Host
}

// TestOrgCreationProvisionsDefaultZone proves the default-zone hook Task 13
// adds: an organization created through the real POST /orgs route (which
// goes through tenancy.Service.CreateOrg, not apitest's SeedOrg shortcut)
// already has its "default" zone the moment creation succeeds — nothing
// else has to create one first.
func TestOrgCreationProvisionsDefaultZone(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("hostzone-owner@example.test", "correct horse battery")
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs", map[string]any{"slug": "hostzone-org", "name": "Host Zone Org"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create org: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/hostzone-org/zones", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list zones: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var listed struct {
		Zones []zoneDTO `json:"zones"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Zones) != 1 || listed.Zones[0].Slug != "default" {
		t.Fatalf("zones = %+v, want exactly one default zone", listed.Zones)
	}
}

// TestOwnerCreatesZoneAndHost walks the brief's step 4 happy path: an owner
// creates a zone, then a host in it, and reads both back.
func TestOwnerCreatesZoneAndHost(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-owner@example.test", "correct horse battery")
	s.SeedOrg("host-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-org/zones", map[string]any{"slug": "fra", "name": "Falkenstein"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create zone: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	zone := decodeZone(t, resp)
	if zone.Slug != "fra" || zone.Name != "Falkenstein" {
		t.Fatalf("zone = %+v", zone)
	}

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-org/hosts", map[string]any{
		"name": "web-01.fra", "address": "10.0.0.1", "zone_id": zone.ID, "managed": true, "monitored": false,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	h := decodeHost(t, resp)
	if h.Name != "web-01.fra" || h.Address != "10.0.0.1" || h.ZoneID != zone.ID || !h.Managed || h.Monitored {
		t.Fatalf("host = %+v", h)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-org/hosts/"+h.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got := decodeHost(t, resp)
	if got.ID != h.ID {
		t.Fatalf("get host = %+v, want id %s", got, h.ID)
	}
}

// TestViewerCanListButNotCreate proves the PermHostsRead/PermHostsWrite
// floor: a viewer can read zones and hosts but gets 403 trying to create
// either.
func TestViewerCanListButNotCreate(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("host-perm-org", owner)
	viewer := s.SeedUser("host-perm-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	c := s.LoginAs(viewer)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-perm-org/zones", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list zones: status %d, want 200", resp.StatusCode)
	}
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-perm-org/hosts", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list hosts: status %d, want 200", resp.StatusCode)
	}
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-perm-org/zones", map[string]any{"slug": "fra", "name": "Falkenstein"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create zone: status %d, want 403", resp.StatusCode)
	}
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-perm-org/hosts", map[string]any{"name": "web-01", "zone_id": "00000000-0000-0000-0000-000000000000"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create host: status %d, want 403", resp.StatusCode)
	}
}

// TestHostInAnotherOrgIsInvisible proves a host id valid in org B reports
// 404 — not 403 — when looked up through org A: it must be invisible, not
// merely forbidden.
func TestHostInAnotherOrgIsInvisible(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("host-iso-owner-a@example.test", "correct horse battery")
	s.SeedOrg("host-iso-org-a", ownerA)
	cA := s.LoginAs(ownerA)

	ownerB := s.SeedUser("host-iso-owner-b@example.test", "correct horse battery")
	s.SeedOrg("host-iso-org-b", ownerB)
	cB := s.LoginAs(ownerB)

	resp := postJSON(t, cB, s.HTTP.URL+"/api/v1/orgs/host-iso-org-b/zones", map[string]any{"slug": "fra", "name": "F"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create zone in org B: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	zoneB := decodeZone(t, resp)
	resp = postJSON(t, cB, s.HTTP.URL+"/api/v1/orgs/host-iso-org-b/hosts", map[string]any{"name": "web-01", "zone_id": zoneB.ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host in org B: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	hostB := decodeHost(t, resp)

	resp = requestJSON(t, cA, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-iso-org-a/hosts/"+hostB.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org A reading org B's host: status %d, want 404", resp.StatusCode)
	}
	resp = requestJSON(t, cA, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/host-iso-org-a/hosts/"+hostB.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org A deleting org B's host: status %d, want 404", resp.StatusCode)
	}

	// A zone from org B must be just as unusable from org A: creating a
	// host in org A that points at org B's zone id must fail as "not
	// found", not silently succeed across the tenant boundary.
	resp = postJSON(t, cA, s.HTTP.URL+"/api/v1/orgs/host-iso-org-a/hosts", map[string]any{"name": "web-02", "zone_id": zoneB.ID})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org A using org B's zone: status %d, want 404, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestHostNameConflictIsReportedAsConflict proves a duplicate host name
// within one org is a 409, not a generic 500.
func TestHostNameConflictIsReportedAsConflict(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-conflict-owner@example.test", "correct horse battery")
	s.SeedOrg("host-conflict-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-conflict-org/zones", nil)
	zones := decodeZoneList(t, resp)
	if len(zones) == 0 {
		t.Fatalf("expected a default zone from SeedOrg: %+v", zones)
	}
	zoneID := zones[0].ID

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-conflict-org/hosts", map[string]any{"name": "web-01", "zone_id": zoneID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-conflict-org/hosts", map[string]any{"name": "web-01", "zone_id": zoneID})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate host name: status %d, want 409, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestHostNameValidation proves an invalid host name is a 400, not a 500 or
// a silently accepted value.
func TestHostNameValidation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-invalid-owner@example.test", "correct horse battery")
	s.SeedOrg("host-invalid-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-invalid-org/zones", nil)
	zones := decodeZoneList(t, resp)

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-invalid-org/hosts", map[string]any{"name": "Not_Valid_Name", "zone_id": zones[0].ID})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid host name: status %d, want 400, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func decodeZoneList(t *testing.T, resp *http.Response) []zoneDTO {
	t.Helper()
	var out struct {
		Zones []zoneDTO `json:"zones"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Zones
}

// TestUpdateHostFullReplace drives the real PATCH /hosts/{id} route (full
// replacement, distinct from PATCH /hosts/{id}/capabilities): every
// mutable field changes in one call, a malformed host id is a 400, and
// moving a host to a zone id from another organization is a 404 — the same
// "a zone from another org is unusable" guarantee CreateHost gives,
// exercised here for UpdateHost through the real router.
func TestUpdateHostFullReplace(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-update-owner@example.test", "correct horse battery")
	s.SeedOrg("host-update-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-update-org/zones", nil)
	zones := decodeZoneList(t, resp)
	defaultZoneID := zones[0].ID

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-update-org/zones", map[string]any{"slug": "fra", "name": "Falkenstein"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second zone: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	newZone := decodeZone(t, resp)

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-update-org/hosts", map[string]any{"name": "web-01", "zone_id": defaultZoneID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	h := decodeHost(t, resp)

	resp = requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/host-update-org/hosts/"+h.ID, map[string]any{
		"name": "web-01-renamed", "address": "10.0.0.9", "zone_id": newZone.ID, "managed": true, "monitored": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	updated := decodeHost(t, resp)
	if updated.Name != "web-01-renamed" || updated.Address != "10.0.0.9" || updated.ZoneID != newZone.ID || !updated.Managed || !updated.Monitored {
		t.Fatalf("updated host = %+v", updated)
	}

	resp = requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/host-update-org/hosts/not-a-uuid", map[string]any{
		"name": "x", "zone_id": defaultZoneID,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed host id: status %d, want 400", resp.StatusCode)
	}

	ownerB := s.SeedUser("host-update-owner-b@example.test", "correct horse battery")
	s.SeedOrg("host-update-org-b", ownerB)
	cB := s.LoginAs(ownerB)
	resp = requestJSON(t, cB, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-update-org-b/zones", nil)
	zonesB := decodeZoneList(t, resp)
	resp = requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/host-update-org/hosts/"+h.ID, map[string]any{
		"name": "web-01-moved", "zone_id": zonesB[0].ID,
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update host to a foreign-org zone: status %d, want 404, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestCapabilitiesPatchFlipsOnlyMentionedFlag drives the real
// PATCH /hosts/{id}/capabilities route end to end: setting only Monitored
// must leave Managed untouched, proving the two capability flags are
// genuinely independent through the whole HTTP stack, not just at the
// service layer (hosts/service_test.go's TestCapabilitiesAreIndependent
// already covers that).
func TestCapabilitiesPatchFlipsOnlyMentionedFlag(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-cap-owner@example.test", "correct horse battery")
	s.SeedOrg("host-cap-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-cap-org/zones", nil)
	zones := decodeZoneList(t, resp)

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-cap-org/hosts", map[string]any{
		"name": "k3s-01", "zone_id": zones[0].ID, "managed": true, "monitored": true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	h := decodeHost(t, resp)

	resp = requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/host-cap-org/hosts/"+h.ID+"/capabilities", map[string]any{"monitored": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch capabilities: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	updated := decodeHost(t, resp)
	if !updated.Managed {
		t.Fatalf("managed flipped by a request that only mentioned monitored: %+v", updated)
	}
	if updated.Monitored {
		t.Fatalf("monitored was not cleared: %+v", updated)
	}

	// Re-fetching independently confirms the PATCH's response was not just
	// an echo of the request — the store actually holds these values.
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-cap-org/hosts/"+h.ID, nil)
	refetched := decodeHost(t, resp)
	if !refetched.Managed || refetched.Monitored {
		t.Fatalf("refetched host = %+v", refetched)
	}
}

// TestActivityFeedShowsCreateHost proves the write is audited with the
// expected operation id and object type, mirroring
// TestAuditRecordsRealMutation (audit_handlers_test.go) for this task's own
// route.
func TestActivityFeedShowsCreateHost(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-audit-owner@example.test", "correct horse battery")
	s.SeedOrg("host-audit-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-audit-org/zones", nil)
	zones := decodeZoneList(t, resp)

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-audit-org/hosts", map[string]any{"name": "web-01", "zone_id": zones[0].ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	h := decodeHost(t, resp)

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-audit-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	activity := decodeActivity(t, resp)
	var found bool
	for _, ev := range activity.Items {
		if ev.Action == "POST createHost" {
			found = true
			if ev.ObjectType != "host" || ev.ObjectID != h.ID {
				t.Fatalf("event = %+v, want object_type=host object_id=%s", ev, h.ID)
			}
		}
	}
	if !found {
		t.Fatalf("activity = %+v, want a POST createHost event", activity.Items)
	}
}

// TestListHostsRejectsMalformedCursor drives a garbage ?cursor= value
// through the real GET /hosts route and asserts it answers 400, not 500 —
// hosts.ErrInvalidCursor was not mapped in mapHostsErr before Task 13 fix
// round 1, finding 1, so this exact request fell through to the default
// branch and answered 500 (logged server-side as "unmapped hosts error").
// Mirrors TestActivityFeedRejectsMalformedQuery's cursor case
// (audit_handlers_test.go), which already covered the identical case for
// the activity feed.
func TestListHostsRejectsMalformedCursor(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-badcursor-owner@example.test", "correct horse battery")
	s.SeedOrg("host-badcursor-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-badcursor-org/hosts?cursor=not-a-valid-cursor", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed cursor: status %d, want 400, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestActivityFeedShowsCreateZone proves POST /zones is audited with
// object_type "zone", not "host" — Task 13 fix round 1's adjacent fix.
// TestActivityFeedShowsCreateHost (above) already covers createHost's own
// object type; this is its zone counterpart, the case the original bug
// actually affected.
func TestActivityFeedShowsCreateZone(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("zone-audit-owner@example.test", "correct horse battery")
	s.SeedOrg("zone-audit-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/zone-audit-org/zones", map[string]any{"slug": "fra", "name": "Falkenstein"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create zone: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	zone := decodeZone(t, resp)

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/zone-audit-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	activity := decodeActivity(t, resp)
	var found bool
	for _, ev := range activity.Items {
		if ev.Action == "POST createZone" {
			found = true
			if ev.ObjectType != "zone" || ev.ObjectID != zone.ID {
				t.Fatalf("event = %+v, want object_type=zone object_id=%s", ev, zone.ID)
			}
		}
	}
	if !found {
		t.Fatalf("activity = %+v, want a POST createZone event", activity.Items)
	}
}

// TestListHostsFiltersAndPagination drives GET /hosts through the real
// router with zone/managed/monitored/q filters and a small page size, so
// keyset pagination is exercised across the HTTP boundary too (the store's
// own pagination is already covered directly by
// TestSQLStoreListHostsPaginatesAcrossPages).
func TestListHostsFiltersAndPagination(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("host-list-owner@example.test", "correct horse battery")
	s.SeedOrg("host-list-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-list-org/zones", nil)
	zones := decodeZoneList(t, resp)
	zoneID := zones[0].ID

	names := []string{"a-host", "b-host", "c-host", "d-host", "e-host"}
	for i, name := range names {
		body := map[string]any{"name": name, "zone_id": zoneID, "managed": i%2 == 0, "monitored": i%2 == 1}
		resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/host-list-org/hosts", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status %d, body %s", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}

	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		url := s.HTTP.URL + "/api/v1/orgs/host-list-org/hosts?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp := requestJSON(t, c, http.MethodGet, url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list page %d: status %d, body %s", page, resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Hosts      []hostDTO `json:"hosts"`
			NextCursor string    `json:"next_cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if len(out.Hosts) == 0 {
			break
		}
		for _, h := range out.Hosts {
			seen = append(seen, h.Name)
		}
		cursor = out.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(names) {
		t.Fatalf("saw %v across all pages, want all of %v", seen, names)
	}
	for i, name := range names {
		if seen[i] != name {
			t.Fatalf("page order = %v, want alphabetical %v", seen, names)
		}
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-list-org/hosts?managed=true", nil)
	var managedOnly struct {
		Hosts []hostDTO `json:"hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&managedOnly); err != nil {
		t.Fatal(err)
	}
	if len(managedOnly.Hosts) != 3 {
		t.Fatalf("managed=true filter: %+v, want 3 hosts", managedOnly.Hosts)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-list-org/hosts?q=c-host", nil)
	var queried struct {
		Hosts []hostDTO `json:"hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queried); err != nil {
		t.Fatal(err)
	}
	if len(queried.Hosts) != 1 || queried.Hosts[0].Name != "c-host" {
		t.Fatalf("q filter: %+v", queried.Hosts)
	}
}

// TestDeleteHostScopedToOrg proves DELETE /hosts/{id} for a host belonging
// to a different org 404s and leaves the host untouched, while deleting it
// through its own org actually removes it.
func TestDeleteHostScopedToOrg(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("host-del-owner-a@example.test", "correct horse battery")
	s.SeedOrg("host-del-org-a", ownerA)
	cA := s.LoginAs(ownerA)
	resp := requestJSON(t, cA, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-del-org-a/zones", nil)
	zonesA := decodeZoneList(t, resp)
	resp = postJSON(t, cA, s.HTTP.URL+"/api/v1/orgs/host-del-org-a/hosts", map[string]any{"name": "web-01", "zone_id": zonesA[0].ID})
	h := decodeHost(t, resp)

	ownerB := s.SeedUser("host-del-owner-b@example.test", "correct horse battery")
	s.SeedOrg("host-del-org-b", ownerB)
	cB := s.LoginAs(ownerB)

	resp = requestJSON(t, cB, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/host-del-org-b/hosts/"+h.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete from wrong org: status %d, want 404", resp.StatusCode)
	}
	resp = requestJSON(t, cA, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-del-org-a/hosts/"+h.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("host must survive a wrong-org delete attempt")
	}

	resp = requestJSON(t, cA, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/host-del-org-a/hosts/"+h.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, cA, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/host-del-org-a/hosts/"+h.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal("host must be gone after delete")
	}
}

// TestFirstRunWizardProvisionsDefaultZone is
// TestOrgCreationProvisionsDefaultZone for the other org-creation path: the
// first-run wizard (setup.Service.Complete). Task 13's brief calls this out
// explicitly — the wizard creates an organization too, and it must not be
// left without its zone either. It reuses the same client c throughout
// (mirroring TestSetupStatusCompleteFlow, setup_handlers_test.go): the
// wizard's own response sets an MFA-complete session cookie in c's jar, so
// no separate viewer account is needed to read the new org's zones back.
func TestFirstRunWizardProvisionsDefaultZone(t *testing.T) {
	s := apitest.New(t)
	c := s.Client()

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("wizard-admin@example.test", "wizard-org"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("complete setup: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/wizard-org/zones", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list zones: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var listed struct {
		Zones []zoneDTO `json:"zones"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Zones) != 1 || listed.Zones[0].Slug != "default" {
		t.Fatalf("zones = %+v, want exactly one default zone", listed.Zones)
	}
}
