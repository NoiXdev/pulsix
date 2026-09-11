package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func requestWithHeaders(t *testing.T, c *http.Client, method, url string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

type groupBody struct {
	Group struct {
		ID       string           `json:"id"`
		Name     string           `json:"name"`
		ParentID *string          `json:"parent_id"`
		Revision int64            `json:"revision"`
		Vars     map[string]any   `json:"vars"`
		Problems []map[string]any `json:"problems"`
		Orphaned []string         `json:"orphaned"`
	} `json:"group"`
	IncompleteMembers int `json:"incomplete_members"`
}

type problemBody struct {
	Errors []struct {
		Location string `json:"location"`
		Message  string `json:"message"`
	} `json:"errors"`
}

func hasLocation(t *testing.T, resp *http.Response, location string) {
	t.Helper()
	var p problemBody
	decodeInto(t, resp, &p)
	for _, e := range p.Errors {
		if e.Location == location {
			return
		}
	}
	t.Fatalf("no error at %q in %+v", location, p.Errors)
}

type invFixture struct {
	s     *apitest.Server
	c     *http.Client
	orgID uuid.UUID
	base  string // .../api/v1/orgs/<slug>
	zone  string
}

func newInvFixture(t *testing.T, slug string) invFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	orgID := s.SeedOrg(slug, owner)
	f := invFixture{s: s, c: s.LoginAs(owner), orgID: orgID, base: s.HTTP.URL + "/api/v1/orgs/" + slug}
	f.zone = decodeZone(t, postJSON(t, f.c, f.base+"/zones", map[string]any{"slug": "fra", "name": "fra"})).ID
	return f
}

func (f invFixture) group(t *testing.T, name string, parent *string) groupBody {
	t.Helper()
	body := map[string]any{"name": name}
	if parent != nil {
		body["parent_id"] = *parent
	}
	resp := postJSON(t, f.c, f.base+"/inventory/groups", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group %s: %d %s", name, resp.StatusCode, mustReadBody(t, resp))
	}
	if resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q", resp.Header.Get("ETag"))
	}
	var g groupBody
	decodeInto(t, resp, &g)
	return g
}

func (f invFixture) host(t *testing.T, name string, managed bool) string {
	t.Helper()
	return decodeHost(t, postJSON(t, f.c, f.base+"/hosts", map[string]any{"name": name, "zone_id": f.zone, "managed": managed})).ID
}

var webSchemaBody = map[string]any{"schema": map[string]any{"fields": []any{
	map[string]any{"name": "env", "type": "enum", "options": []any{"prod", "staging"}, "required": true},
	map[string]any{"name": "db_password", "type": "secret"},
	map[string]any{"name": "php_default", "type": "enum", "options": []any{"8.3", "8.4"}, "default": "8.3", "overridable": true},
	map[string]any{"name": "vhosts", "type": "repeater", "scope": "host", "required": true, "min_items": 1, "fields": []any{
		map[string]any{"name": "domain", "type": "string", "required": true, "pattern": "^[a-z0-9.-]+$"},
		map[string]any{"name": "basic_auth", "type": "object", "fields": []any{
			map[string]any{"name": "user", "type": "string"},
			map[string]any{"name": "password", "type": "secret"},
		}},
	}},
}}}

func TestInventoryGroupsAndConcurrency(t *testing.T) {
	f := newInvFixture(t, "inv-groups")
	prod := f.group(t, "production", nil)
	web := f.group(t, "webserver", &prod.Group.ID)
	url := f.base + "/inventory/groups/" + web.Group.ID

	if resp := requestJSON(t, f.c, http.MethodPatch, url, map[string]any{"name": "web"}); resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", resp.StatusCode)
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "web"}, map[string]string{"If-Match": `"5"`}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", resp.StatusCode)
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "web"}, map[string]string{"If-Match": "abc"}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("malformed If-Match: %d", resp.StatusCode)
	}
	resp := requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "web", "parent_id": prod.Group.ID}, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("rename: %d %q %s", resp.StatusCode, resp.Header.Get("ETag"), mustReadBody(t, resp))
	}
	prodURL := f.base + "/inventory/groups/" + prod.Group.ID
	if resp := requestWithHeaders(t, f.c, http.MethodPatch, prodURL, map[string]any{"name": "production", "parent_id": web.Group.ID}, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cycle: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, prodURL, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete a parent: %d", resp.StatusCode)
	}
	if resp := postJSON(t, f.c, f.base+"/inventory/groups", map[string]any{"name": "all"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reserved name: %d", resp.StatusCode)
	}
	if resp := postJSON(t, f.c, f.base+"/inventory/groups", map[string]any{"name": "production"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate: %d", resp.StatusCode)
	}
	var list struct {
		Groups []struct {
			Name    string `json:"name"`
			Members int    `json:"members"`
		} `json:"groups"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base+"/inventory/groups", nil), &list)
	if len(list.Groups) != 2 || list.Groups[0].Name != "production" || list.Groups[1].Name != "web" {
		t.Fatalf("list = %+v", list)
	}
	resp = requestJSON(t, f.c, http.MethodGet, url, nil)
	if resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("GET ETag = %q", resp.Header.Get("ETag"))
	}
}

func TestInventorySchemaAndValues(t *testing.T) {
	f := newInvFixture(t, "inv-schema")
	g := f.group(t, "webserver", nil)
	url := f.base + "/inventory/groups/" + g.Group.ID
	v1 := map[string]string{"If-Match": `"1"`}

	bad := map[string]any{"schema": map[string]any{"fields": []any{map[string]any{"name": "ansible_x", "type": "string"}}}}
	resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/schema", bad, v1)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid schema: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	hasLocation(t, resp, "body.schema.fields[0].name")
	unknownKey := map[string]any{"schema": map[string]any{"fields": []any{map[string]any{"name": "x", "type": "string", "colour": "red"}}}}
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/schema", unknownKey, v1); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown schema attribute: %d", resp.StatusCode)
	}

	resp = requestWithHeaders(t, f.c, http.MethodPut, url+"/schema", webSchemaBody, v1)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("schema: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var withSchema groupBody
	decodeInto(t, resp, &withSchema)
	if len(withSchema.Group.Problems) != 1 {
		t.Fatalf("the required group field env must show as a problem: %+v", withSchema.Group.Problems)
	}

	resp = requestWithHeaders(t, f.c, http.MethodPut, url+"/vars", map[string]any{"vars": map[string]any{"env": "prod", "db_password": "grp-hunter2"}}, map[string]string{"If-Match": `"2"`})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "hunter2") || !strings.Contains(body, `"$secret":"set"`) {
		t.Fatalf("vars: %d %s", resp.StatusCode, body)
	}
	keep := map[string]any{"vars": map[string]any{"env": "staging", "db_password": map[string]any{"$secret": "keep", "from": "db_password"}}}
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/vars", keep, map[string]string{"If-Match": `"3"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("keep: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestWithHeaders(t, f.c, http.MethodPut, url+"/vars", map[string]any{"vars": map[string]any{"env": "prod", "extra": 1}}, map[string]string{"If-Match": `"4"`})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown value: %d", resp.StatusCode)
	}
	hasLocation(t, resp, "body.vars.extra")
}

func TestInventoryMembers(t *testing.T) {
	f := newInvFixture(t, "inv-members")
	g := f.group(t, "webserver", nil)
	url := f.base + "/inventory/groups/" + g.Group.ID
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/schema", webSchemaBody, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	managed, unmanaged := f.host(t, "web-01", true), f.host(t, "switch-01", false)

	if resp := requestJSON(t, f.c, http.MethodPut, url+"/members/"+unmanaged, map[string]any{"vars": map[string]any{}}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("unmanaged host: %d", resp.StatusCode)
	}
	resp := requestJSON(t, f.c, http.MethodPut, url+"/members/"+managed, map[string]any{"vars": map[string]any{"vhosts": []any{map[string]any{"domain": "Bad Domain"}}}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid member values: %d", resp.StatusCode)
	}
	hasLocation(t, resp, "body.vars.vhosts[0].domain")

	vars := map[string]any{"vars": map[string]any{"php_default": "8.4", "vhosts": []any{
		map[string]any{"domain": "acme.io", "basic_auth": map[string]any{"user": "u", "password": "mem-hunter2"}},
	}}}
	resp = requestJSON(t, f.c, http.MethodPut, url+"/members/"+managed, vars)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"1"` || strings.Contains(body, "hunter2") {
		t.Fatalf("assign: %d %q %s", resp.StatusCode, resp.Header.Get("ETag"), body)
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/members/"+managed, vars, map[string]string{"If-Match": `"9"`}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale member revision: %d", resp.StatusCode)
	}
	var members struct {
		Members []struct {
			HostName string `json:"host_name"`
			Complete bool   `json:"complete"`
		} `json:"members"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url+"/members", nil), &members)
	if len(members.Members) != 1 || members.Members[0].HostName != "web-01" || !members.Members[0].Complete {
		t.Fatalf("members = %+v", members)
	}
	var hostGroups struct {
		Groups []struct {
			GroupName string `json:"group_name"`
		} `json:"groups"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.base+"/hosts/"+managed+"/inventory/groups", nil), &hostGroups)
	if len(hostGroups.Groups) != 1 || hostGroups.Groups[0].GroupName != "webserver" {
		t.Fatalf("host groups = %+v", hostGroups)
	}
	resp = requestJSON(t, f.c, http.MethodGet, f.base+"/hosts/"+managed+"/inventory/preview", nil)
	body = mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"password":"********"`) || !strings.Contains(body, `"ansible_host":"web-01"`) || strings.Contains(body, "hunter2") {
		t.Fatalf("host preview: %d %s", resp.StatusCode, body)
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, url+"/members/"+managed, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unassign: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, url+"/members/"+managed, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second unassign: %d", resp.StatusCode)
	}
}

func TestInventoryPermissionsAndIsolation(t *testing.T) {
	f := newInvFixture(t, "inv-perm")
	g := f.group(t, "webserver", nil)
	viewer := f.s.SeedUser("inv-perm-viewer@example.test", "correct horse battery")
	f.s.AddMember(f.orgID, viewer, tenancy.RoleViewer)
	view := f.s.LoginAs(viewer)
	url := f.base + "/inventory/groups/" + g.Group.ID
	if resp := requestJSON(t, view, http.MethodGet, url, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer reads a group: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, view, http.MethodGet, f.base+"/inventory/preview", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer reads the preview: %d", resp.StatusCode)
	}
	if resp := requestWithHeaders(t, view, http.MethodPut, url+"/vars", map[string]any{"vars": map[string]any{}}, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer writes values: %d", resp.StatusCode)
	}

	// The group is invisible from another organization, not merely forbidden.
	otherOwner := f.s.SeedUser("inv-perm-other@example.test", "correct horse battery")
	f.s.SeedOrg("inv-perm-other", otherOwner)
	other := f.s.HTTP.URL + "/api/v1/orgs/inv-perm-other/inventory/groups/" + g.Group.ID
	if resp := requestJSON(t, f.s.LoginAs(otherOwner), http.MethodGet, other, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a group of another organization: %d", resp.StatusCode)
	}
}

// TestHostInventoryGroupsMasksSecrets pins that a host's group list returns
// its membership secrets as {"$secret":"set"}, never plaintext or a sealed
// envelope.
func TestHostInventoryGroupsMasksSecrets(t *testing.T) {
	f := newInvFixture(t, "inv-host-groups")
	g := f.group(t, "webserver", nil)
	url := f.base + "/inventory/groups/" + g.Group.ID
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/schema", webSchemaBody, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	host := f.host(t, "web-01", true)
	if resp := requestJSON(t, f.c, http.MethodPut, url+"/members/"+host, map[string]any{"vars": map[string]any{"vhosts": []any{
		map[string]any{"domain": "acme.io", "basic_auth": map[string]any{"user": "u", "password": "host-groups-hunter2"}},
	}}}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	resp := requestJSON(t, f.c, http.MethodGet, f.base+"/hosts/"+host+"/inventory/groups", nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "hunter2") || strings.Contains(body, "$sealed") {
		t.Fatalf("host groups: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Groups []struct {
			Vars map[string]any `json:"vars"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 1 {
		t.Fatalf("groups = %s", body)
	}
	pw := out.Groups[0].Vars["vhosts"].([]any)[0].(map[string]any)["basic_auth"].(map[string]any)["password"]
	if !reflect.DeepEqual(pw, map[string]any{"$secret": "set"}) {
		t.Fatalf("password = %#v, want the mask", pw)
	}
}
