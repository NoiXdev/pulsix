// The check definition routes' tests (v0.3b-1): the CRUD surface, its role
// matrix, cross-organization invisibility, the validation the service owns,
// the write-path push to the affected hosts and the audit trail — all driven
// through the full API against the test database. The monitoring group
// routes' tests live in monitoring_group_handlers_test.go and the per-host
// reads' in host_check_handlers_test.go, both on the fixture seeded here.
package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type checkDTO struct {
	ID              string         `json:"id"`
	Key             string         `json:"key"`
	Plugin          string         `json:"plugin"`
	Params          map[string]any `json:"params"`
	IntervalSeconds int            `json:"interval_seconds"`
	Enabled         bool           `json:"enabled"`
	Origin          string         `json:"origin"`
	BindingKind     string         `json:"binding_kind"`
	GroupID         string         `json:"group_id"`
	HostID          string         `json:"host_id"`
}

// checksFixture is the seeded organization every checks test shares: an
// owner, an operator and a viewer; three hosts — two monitored, one not,
// tagged so a monitoring group's selector has something to match; and the
// "Linux base" seed set every organization is created with.
type checksFixture struct {
	s         *apitest.Server
	orgID     uuid.UUID
	orgSlug   string
	ownerC    *http.Client
	operatorC *http.Client
	viewerC   *http.Client
	stranger  *http.Client
	// web and dbHost are monitored; cold is not.
	web, dbHost, cold uuid.UUID
}

func newChecksFixture(t *testing.T, slug string) checksFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	stranger := s.SeedUser(slug+"-stranger@example.test", "correct horse battery")
	org := s.SeedOrg(slug, owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	scoped := db.WithOrgScope(context.Background(), org)

	zones, err := s.Deps.Hosts.ListZones(scoped, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	zone := zones[0].ID
	web := seedCheckHost(t, s, scoped, org, zone, "web-1", "10.0.0.1", true, "web")
	dbHost := seedCheckHost(t, s, scoped, org, zone, "db-1", "10.0.0.2", true, "db")
	cold := seedCheckHost(t, s, scoped, org, zone, "cold-1", "10.0.0.3", false, "")

	f := checksFixture{
		s: s, orgID: org, orgSlug: slug,
		ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		stranger: s.LoginAs(stranger),
		web:      web, dbHost: dbHost, cold: cold,
	}
	// The fixture's own writes are not what any test is asserting on.
	s.Push.Reset()
	return f
}

func seedCheckHost(t *testing.T, s *apitest.Server, ctx context.Context, org, zone uuid.UUID, name, addr string, monitored bool, role string) uuid.UUID {
	t.Helper()
	h, err := s.Deps.Hosts.CreateHost(ctx, org, hosts.HostInput{
		Name: name, Address: addr, ZoneID: zone, Managed: true, Monitored: monitored,
	})
	if err != nil {
		t.Fatal(err)
	}
	if role != "" {
		if err := s.Deps.Hosts.SetTags(ctx, org, h.ID, []hosts.Tag{{Key: "role", Value: role, Source: hosts.TagSourceManual}}); err != nil {
			t.Fatal(err)
		}
	}
	return h.ID
}

func (f checksFixture) checksURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/checks"
}

func (f checksFixture) groupsURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/monitoring-groups"
}

func (f checksFixture) hostURL(id uuid.UUID) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + id.String()
}

// listChecks reads the organization's definitions through the API.
func (f checksFixture) listChecks(t *testing.T, c *http.Client) []checkDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.checksURL(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Checks []checkDTO `json:"checks"`
	}
	decodeInto(t, resp, &out)
	return out.Checks
}

// createCheck posts body and expects 201, returning the created row.
func (f checksFixture) createCheck(t *testing.T, c *http.Client, body map[string]any) checkDTO {
	t.Helper()
	resp := postJSON(t, c, f.checksURL(), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create check = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Check checkDTO `json:"check"`
	}
	decodeInto(t, resp, &out)
	return out.Check
}

// TestCheckListReportsTheSeedSet: every organization is created with the
// "Linux base" set, and the list route is what the Checks page reads. A
// viewer sees it — knowing what is measured is part of seeing the fleet.
func TestCheckListReportsTheSeedSet(t *testing.T) {
	f := newChecksFixture(t, "checks-seed")
	list := f.listChecks(t, f.viewerC)
	byKey := map[string]checkDTO{}
	for _, d := range list {
		byKey[d.Key] = d
	}
	for _, key := range []string{"cpu", "memory", "disk", "service"} {
		d, ok := byKey[key]
		if !ok {
			t.Fatalf("seed key %q missing from %+v", key, list)
		}
		if d.Origin != "seed" || d.BindingKind != "org" || !d.Enabled {
			t.Fatalf("seed check %q = %+v, want an enabled, org-bound seed row", key, d)
		}
		if d.Params == nil {
			t.Fatalf("seed check %q has null params; the GUI binds an object", key)
		}
	}
	if got := byKey["service"].Params["units"]; got == nil {
		t.Fatalf("service seed params = %+v, want the units list", byKey["service"].Params)
	}
}

// TestCheckDefinitionLifecycle walks the whole surface an operator uses:
// create, read back, replace, delete, and the 404 that follows.
func TestCheckDefinitionLifecycle(t *testing.T) {
	f := newChecksFixture(t, "checks-life")
	created := f.createCheck(t, f.operatorC, map[string]any{
		"key": "disk-var", "plugin": "disk", "interval_seconds": 120,
		"params": map[string]any{"mounts": []string{"/var"}}, "binding_kind": "host", "host_id": f.web.String(),
	})
	if created.Origin != "custom" || created.HostID != f.web.String() || !created.Enabled {
		t.Fatalf("created = %+v, want a custom, host-bound, enabled check", created)
	}
	if created.IntervalSeconds != 120 {
		t.Fatalf("interval = %d, want 120", created.IntervalSeconds)
	}

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.checksURL()+"/"+created.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer get = %d", resp.StatusCode)
	}
	var single struct {
		Check checkDTO `json:"check"`
	}
	decodeInto(t, resp, &single)
	if single.Check.Key != "disk-var" {
		t.Fatalf("get = %+v", single.Check)
	}

	disabled := false
	resp = requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+created.ID, map[string]any{
		"key": "disk-var", "plugin": "disk", "interval_seconds": 600,
		"params":       map[string]any{"mounts": []string{"/var", "/srv"}},
		"binding_kind": "host", "host_id": f.web.String(), "enabled": disabled,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &single)
	if single.Check.IntervalSeconds != 600 || single.Check.Enabled {
		t.Fatalf("updated = %+v, want a 600s disabled check", single.Check)
	}

	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.checksURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", resp.StatusCode)
	}
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete twice = %d, want 404", resp.StatusCode)
	}
}

// TestCheckRoleMatrix pins who may do what on every route: a viewer reads
// and an operator writes (checks.read / checks.write, mirroring agents.*),
// and somebody who is not a member of the organization at all sees
// nothing.
func TestCheckRoleMatrix(t *testing.T) {
	f := newChecksFixture(t, "checks-roles")
	existing := f.listChecks(t, f.viewerC)[0]
	body := map[string]any{"plugin": "cpu", "key": "cpu-fast", "interval_seconds": 15, "binding_kind": "org"}

	for _, c := range []struct {
		name           string
		method, url    string
		body           map[string]any
		viewer, member int
	}{
		{"list", http.MethodGet, f.checksURL(), nil, http.StatusOK, http.StatusForbidden},
		{"get", http.MethodGet, f.checksURL() + "/" + existing.ID, nil, http.StatusOK, http.StatusForbidden},
		{"create", http.MethodPost, f.checksURL(), body, http.StatusForbidden, http.StatusForbidden},
		{"update", http.MethodPut, f.checksURL() + "/" + existing.ID, body, http.StatusForbidden, http.StatusForbidden},
		{"delete", http.MethodDelete, f.checksURL() + "/" + existing.ID, nil, http.StatusForbidden, http.StatusForbidden},
	} {
		if resp := requestJSON(t, f.viewerC, c.method, c.url, c.body); resp.StatusCode != c.viewer {
			t.Errorf("viewer %s = %d, want %d", c.name, resp.StatusCode, c.viewer)
		}
		if resp := requestJSON(t, f.stranger, c.method, c.url, c.body); resp.StatusCode != c.member {
			t.Errorf("non-member %s = %d, want %d", c.name, resp.StatusCode, c.member)
		}
	}
	// The same calls an operator makes must succeed, or the matrix above
	// would pass for a route that is simply broken.
	if resp := postJSON(t, f.operatorC, f.checksURL(), body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator create = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestCheckCrossOrgInvisibility: a definition of another organization is
// invisible, not merely refused — its id answers 404 inside an
// organization the caller does belong to, 403 inside the one it actually
// lives in (which the caller is not a member of), and it never appears in
// anyone else's list.
func TestCheckCrossOrgInvisibility(t *testing.T) {
	f := newChecksFixture(t, "checks-iso-a")
	otherOwner := f.s.SeedUser("checks-iso-b-owner@example.test", "correct horse battery")
	f.s.SeedOrg("checks-iso-b", otherOwner)
	otherC := f.s.LoginAs(otherOwner)
	otherURL := f.s.HTTP.URL + "/api/v1/orgs/checks-iso-b/checks"

	resp := postJSON(t, otherC, otherURL, map[string]any{
		"plugin": "cpu", "key": "cpu-other", "interval_seconds": 30, "binding_kind": "org",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create in the other org = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Check checkDTO `json:"check"`
	}
	decodeInto(t, resp, &created)

	update := map[string]any{"plugin": "cpu", "key": "cpu-other", "interval_seconds": 30, "binding_kind": "org"}
	for _, c := range []struct {
		method string
		body   map[string]any
	}{{http.MethodGet, nil}, {http.MethodPut, update}, {http.MethodDelete, nil}} {
		// Inside the caller's own organization, the foreign id simply
		// does not exist.
		if resp := requestJSON(t, f.operatorC, c.method, f.checksURL()+"/"+created.Check.ID, c.body); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s of a foreign id in my own org = %d, want 404", c.method, resp.StatusCode)
		}
		// Inside the organization it belongs to, the caller is not a
		// member: refused before any row is read.
		if resp := requestJSON(t, f.operatorC, c.method, otherURL+"/"+created.Check.ID, c.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s in an org I am not a member of = %d, want 403", c.method, resp.StatusCode)
		}
	}
	for _, d := range f.listChecks(t, f.ownerC) {
		if d.Key == "cpu-other" {
			t.Fatalf("another organization's check is visible: %+v", d)
		}
	}
}

// TestCheckValidationRefusals: every refusal below comes from
// internal/checks, and the route must surface it as a 4xx carrying the
// service's own message rather than a generic 500 or huma's own wording.
func TestCheckValidationRefusals(t *testing.T) {
	f := newChecksFixture(t, "checks-invalid")
	cases := map[string]struct {
		body map[string]any
		want int
	}{
		"unknown plugin":    {map[string]any{"plugin": "nginx", "interval_seconds": 60, "binding_kind": "org"}, http.StatusBadRequest},
		"interval too low":  {map[string]any{"plugin": "cpu", "key": "cpu-fast", "interval_seconds": 1, "binding_kind": "org"}, http.StatusBadRequest},
		"unknown parameter": {map[string]any{"plugin": "cpu", "key": "cpu-x", "interval_seconds": 60, "binding_kind": "org", "params": map[string]any{"nope": 1}}, http.StatusBadRequest},
		"service needs units": {map[string]any{
			"plugin": "service", "key": "svc-x", "interval_seconds": 60, "binding_kind": "org", "params": map[string]any{},
		}, http.StatusBadRequest},
		"group binding without a group": {map[string]any{"plugin": "cpu", "key": "cpu-g", "interval_seconds": 60, "binding_kind": "group"}, http.StatusBadRequest},
		"host binding with a group":     {map[string]any{"plugin": "cpu", "key": "cpu-h", "interval_seconds": 60, "binding_kind": "host", "group_id": uuid.Must(uuid.NewV7()).String()}, http.StatusBadRequest},
		"unknown group": {map[string]any{
			"plugin": "cpu", "key": "cpu-ug", "interval_seconds": 60, "binding_kind": "group", "group_id": uuid.Must(uuid.NewV7()).String(),
		}, http.StatusBadRequest},
		"malformed host id":               {map[string]any{"plugin": "cpu", "key": "cpu-bad", "interval_seconds": 60, "binding_kind": "host", "host_id": "not-a-uuid"}, http.StatusUnprocessableEntity},
		"duplicate key at the same scope": {map[string]any{"plugin": "cpu", "interval_seconds": 60, "binding_kind": "org"}, http.StatusConflict},
	}
	for name, c := range cases {
		resp := postJSON(t, f.operatorC, f.checksURL(), c.body)
		if resp.StatusCode != c.want {
			t.Errorf("%s = %d, want %d (%s)", name, resp.StatusCode, c.want, mustReadBody(t, resp))
		}
	}

	// A seeded row's key and plugin are fixed — the operator copies it to
	// change them — but its interval and enabled flag are not.
	var seeded checkDTO
	for _, d := range f.listChecks(t, f.ownerC) {
		if d.Key == "cpu" {
			seeded = d
		}
	}
	resp := requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+seeded.ID, map[string]any{
		"plugin": "memory", "key": "cpu", "interval_seconds": 60, "binding_kind": "org",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("changing a seeded plugin = %d, want 400", resp.StatusCode)
	}
	resp = requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+seeded.ID, map[string]any{
		"plugin": "cpu", "key": "cpu", "interval_seconds": 300, "binding_kind": "org",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changing a seeded interval = %d %s, want 200", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestCheckWritesPushTheAffectedHosts: the manager recomputes and pushes to
// the hosts a write actually affects (spec, "Configuration delivery"). An
// organization-bound write reaches every monitored host and no other; a
// host-bound write reaches exactly that host.
func TestCheckWritesPushTheAffectedHosts(t *testing.T) {
	f := newChecksFixture(t, "checks-push")

	f.s.Push.Reset()
	f.createCheck(t, f.operatorC, map[string]any{"plugin": "cpu", "key": "cpu-fast", "interval_seconds": 15, "binding_kind": "org"})
	if !f.s.Push.PushedHosts(f.web, f.dbHost) {
		t.Fatalf("an org-bound write pushed %+v, want both monitored hosts", f.s.Push.Pushes())
	}
	for _, p := range f.s.Push.Pushes() {
		if p.HostID == f.cold {
			t.Fatalf("an unmonitored host was pushed: %+v", p)
		}
		if p.OrgID != f.orgID {
			t.Fatalf("push carried org %s, want %s", p.OrgID, f.orgID)
		}
	}

	f.s.Push.Reset()
	hostBound := f.createCheck(t, f.operatorC, map[string]any{
		"plugin": "memory", "key": "memory-host", "interval_seconds": 30, "binding_kind": "host", "host_id": f.web.String(),
	})
	pushes := f.s.Push.Pushes()
	if len(pushes) != 1 || pushes[0].HostID != f.web {
		t.Fatalf("a host-bound write pushed %+v, want only web-1", pushes)
	}

	// Rebinding it to the other host must reach both: the one that loses
	// the check and the one that gains it.
	f.s.Push.Reset()
	resp := requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+hostBound.ID, map[string]any{
		"plugin": "memory", "key": "memory-host", "interval_seconds": 30, "binding_kind": "host", "host_id": f.dbHost.String(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebind = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if !f.s.Push.PushedHosts(f.web, f.dbHost) {
		t.Fatalf("a rebind pushed %+v, want both the old and the new host", f.s.Push.Pushes())
	}

	f.s.Push.Reset()
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+hostBound.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	pushes = f.s.Push.Pushes()
	if len(pushes) != 1 || pushes[0].HostID != f.dbHost {
		t.Fatalf("a delete pushed %+v, want only the host it was bound to", pushes)
	}
}

// TestCheckPushFailureDoesNotFailTheWrite: the write has already committed
// when the push runs, and the agent re-asserts its configuration version
// every 60 seconds — so a push that cannot be delivered is logged, never
// turned into an error the operator would retry (and retry into a
// duplicate-key conflict).
func TestCheckPushFailureDoesNotFailTheWrite(t *testing.T) {
	f := newChecksFixture(t, "checks-push-fail")
	f.s.Push.FailWith(errors.New("no stream for this host"))
	defer f.s.Push.FailWith(nil)

	created := f.createCheck(t, f.operatorC, map[string]any{
		"plugin": "cpu", "key": "cpu-fast", "interval_seconds": 15, "binding_kind": "org",
	})
	// And the write really happened, rather than being rolled back with a
	// 201 in front of it.
	found := false
	for _, d := range f.listChecks(t, f.ownerC) {
		if d.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the created check is not in the list; the write did not survive the failing push")
	}
}

// TestCheckMutationsAreAudited: create, update and delete each leave one
// attributable entry in the organization's activity trail.
func TestCheckMutationsAreAudited(t *testing.T) {
	f := newChecksFixture(t, "checks-audit")
	created := f.createCheck(t, f.operatorC, map[string]any{
		"plugin": "cpu", "key": "cpu-fast", "interval_seconds": 15, "binding_kind": "org",
	})
	if resp := requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+created.ID, map[string]any{
		"plugin": "cpu", "key": "cpu-fast", "interval_seconds": 45, "binding_kind": "org",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+created.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}

	resp := requestJSON(t, f.ownerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/activity?limit=50", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activity = %d", resp.StatusCode)
	}
	activity := decodeActivity(t, resp)
	actions := map[string]int{}
	for _, ev := range activity.Items {
		if ev.ObjectType != "check_definition" {
			continue
		}
		actions[ev.Action]++
		if ev.Summary == "" || ev.ObjectID != created.ID {
			t.Fatalf("event %+v: want a summary and the created check's id", ev)
		}
		if !strings.Contains(ev.Summary, "cpu-fast") {
			t.Fatalf("summary %q does not name the check", ev.Summary)
		}
	}
	// The recorded action is the HTTP method plus the operation id (see
	// auditMiddleware).
	for _, action := range []string{"POST createCheck", "PUT updateCheck", "DELETE deleteCheck"} {
		if actions[action] != 1 {
			t.Fatalf("action %q recorded %d times, want 1; trail = %+v", action, actions[action], activity.Items)
		}
	}
}
