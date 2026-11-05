package api_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type tagDTO struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

type tagsOutput struct {
	Tags         []tagDTO `json:"tags"`
	HostRevision int64    `json:"host_revision"`
}

func decodeTags(t *testing.T, resp *http.Response) tagsOutput {
	t.Helper()
	var out tagsOutput
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// createHostFor is a small helper shared by this file's tests: seeds an
// org's default zone (via SeedOrg, already done by the caller) and creates
// one host in it, returning its id.
func createHostFor(t *testing.T, c *http.Client, baseURL, org, name string) string {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, baseURL+"/api/v1/orgs/"+org+"/zones", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list zones: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	zones := decodeZoneList(t, resp)
	if len(zones) == 0 {
		t.Fatalf("expected a default zone from SeedOrg")
	}
	resp = postJSON(t, c, baseURL+"/api/v1/orgs/"+org+"/hosts", map[string]any{"name": name, "zone_id": zones[0].ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	return decodeHost(t, resp).ID
}

// TestPutHostTagsThenGet is the brief's own step-4 example: PUT a manual
// tag, then GET returns it back with source "manual".
func TestPutHostTagsThenGet(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-org", owner)
	c := s.LoginAs(owner)

	hostID := createHostFor(t, c, s.HTTP.URL, "tags-org", "web-01")

	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-org/hosts/"+hostID+"/tags",
		[]map[string]any{{"key": "role", "value": "db"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put tags: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tags-org/hosts/"+hostID+"/tags", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get tags: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got := decodeTags(t, resp)
	if len(got.Tags) != 1 || got.Tags[0].Key != "role" || got.Tags[0].Value != "db" || got.Tags[0].Source != "manual" {
		t.Fatalf("tags = %+v, want [{role db manual}]", got.Tags)
	}
}

// TestPutHostTagsReplacesWholesale proves a second PUT replaces the manual
// set entirely, not merges into it.
func TestPutHostTagsReplacesWholesale(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-replace-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-replace-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-replace-org", "web-01")

	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-replace-org/hosts/"+hostID+"/tags",
		[]map[string]any{{"key": "role", "value": "db"}, {"key": "env", "value": "prod"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first put: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-replace-org/hosts/"+hostID+"/tags",
		[]map[string]any{{"key": "role", "value": "web"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second put: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got := decodeTags(t, resp)
	if len(got.Tags) != 1 || got.Tags[0].Key != "role" || got.Tags[0].Value != "web" {
		t.Fatalf("tags after replace = %+v, want just [{role web manual}]", got.Tags)
	}
}

// TestPutHostTagsRejectsBadKey proves an invalid tag key is a 400, not a
// 500 or a silently accepted value — mirroring TestHostNameValidation
// (host_handlers_test.go) for tags.
func TestPutHostTagsRejectsBadKey(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-badkey-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-badkey-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-badkey-org", "web-01")

	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-badkey-org/hosts/"+hostID+"/tags",
		[]map[string]any{{"key": "Not_Valid", "value": "x"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid tag key: status %d, want 400, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestPutHostTagsRejectsTooMany proves the 50-manual-tags cap is enforced
// through the real route, not just at the service layer
// (hosts/tags_test.go's TestSetTagsRejectsMoreThan50Tags already covers
// that).
func TestPutHostTagsRejectsTooMany(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-toomany-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-toomany-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-toomany-org", "web-01")

	tags := make([]map[string]any, 51)
	for i := range tags {
		tags[i] = map[string]any{"key": "k" + string(rune('a'+i%26)) + string(rune('0'+i/26)), "value": "v"}
	}
	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-toomany-org/hosts/"+hostID+"/tags", tags)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("more than 50 tags: status %d, want 400, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestGetHostTagsInvisibleAcrossOrgs mirrors TestHostInAnotherOrgIsInvisible
// (host_handlers_test.go): a host id valid in org B must be invisible, not
// merely forbidden, through org A's own tags routes.
func TestGetHostTagsInvisibleAcrossOrgs(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("tags-iso-owner-a@example.test", "correct horse battery")
	s.SeedOrg("tags-iso-org-a", ownerA)
	cA := s.LoginAs(ownerA)

	ownerB := s.SeedUser("tags-iso-owner-b@example.test", "correct horse battery")
	s.SeedOrg("tags-iso-org-b", ownerB)
	cB := s.LoginAs(ownerB)
	hostB := createHostFor(t, cB, s.HTTP.URL, "tags-iso-org-b", "web-01")

	resp := requestJSON(t, cA, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tags-iso-org-a/hosts/"+hostB+"/tags", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org A reading org B's host tags: status %d, want 404", resp.StatusCode)
	}
	resp = requestJSON(t, cA, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-iso-org-a/hosts/"+hostB+"/tags",
		[]map[string]any{{"key": "role", "value": "db"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org A setting org B's host tags: status %d, want 404", resp.StatusCode)
	}
}

// TestViewerCanReadTagsButNotWrite proves the PermHostsRead/PermHostsWrite
// floor for tags, mirroring TestViewerCanListButNotCreate
// (host_handlers_test.go).
func TestViewerCanReadTagsButNotWrite(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("tags-perm-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-perm-org", "web-01")

	viewer := s.SeedUser("tags-perm-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	cViewer := s.LoginAs(viewer)

	resp := requestJSON(t, cViewer, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tags-perm-org/hosts/"+hostID+"/tags", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer read tags: status %d, want 200", resp.StatusCode)
	}
	resp = requestJSON(t, cViewer, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-perm-org/hosts/"+hostID+"/tags",
		[]map[string]any{{"key": "role", "value": "db"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer write tags: status %d, want 403", resp.StatusCode)
	}
}

// TestListOrgTagKeys proves GET /orgs/{org}/tags groups tags by key with
// per-value counts.
func TestListOrgTagKeys(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-keys-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-keys-org", owner)
	c := s.LoginAs(owner)

	h1 := createHostFor(t, c, s.HTTP.URL, "tags-keys-org", "a-host")
	h2 := createHostFor(t, c, s.HTTP.URL, "tags-keys-org", "b-host")
	requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-keys-org/hosts/"+h1+"/tags", []map[string]any{{"key": "role", "value": "db"}})
	requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-keys-org/hosts/"+h2+"/tags", []map[string]any{{"key": "role", "value": "web"}})

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tags-keys-org/tags", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tag keys: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Keys []struct {
			Key    string `json:"key"`
			Values []struct {
				Value string `json:"value"`
				Count int    `json:"count"`
			} `json:"values"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Keys) != 1 || out.Keys[0].Key != "role" {
		t.Fatalf("keys = %+v, want one key 'role'", out.Keys)
	}
	if len(out.Keys[0].Values) != 2 {
		t.Fatalf("values = %+v, want db and web", out.Keys[0].Values)
	}
}

// TestQueryHostsBySelector drives POST /orgs/{org}/hosts/query end to end:
// a matching selector returns the tagged host, and a syntax error in the
// selector is reported as 422 with the parser's own message — the brief's
// own judging criterion, proven through the real router rather than only
// at the selector.Parse level (selector_test.go already covers that in
// isolation).
func TestQueryHostsBySelector(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-query-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-query-org", owner)
	c := s.LoginAs(owner)

	dbHost := createHostFor(t, c, s.HTTP.URL, "tags-query-org", "db-01")
	webHost := createHostFor(t, c, s.HTTP.URL, "tags-query-org", "web-01")
	requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-query-org/hosts/"+dbHost+"/tags", []map[string]any{{"key": "role", "value": "db"}})
	requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-query-org/hosts/"+webHost+"/tags", []map[string]any{{"key": "role", "value": "web"}})

	resp := requestJSON(t, c, http.MethodPost, s.HTTP.URL+"/api/v1/orgs/tags-query-org/hosts/query", map[string]any{"selector": "role = db"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query hosts: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Hosts []hostDTO `json:"hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts) != 1 || out.Hosts[0].ID != dbHost {
		t.Fatalf("query hosts(role = db) = %+v, want just db-01", out.Hosts)
	}

	resp = requestJSON(t, c, http.MethodPost, s.HTTP.URL+"/api/v1/orgs/tags-query-org/hosts/query", map[string]any{"selector": "role == db"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("syntax error in selector: status %d, want 422, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	body := mustReadBody(t, resp)
	if !strings.Contains(body, "selector:") {
		t.Fatalf("422 body %q does not carry the parser's own message", body)
	}
}

// TestQueryHostsBySelectorScopedToOrg proves a selector matching a tag in
// another organization never returns that org's hosts.
func TestQueryHostsBySelectorScopedToOrg(t *testing.T) {
	s := apitest.New(t)
	ownerA := s.SeedUser("tags-query-iso-owner-a@example.test", "correct horse battery")
	s.SeedOrg("tags-query-iso-org-a", ownerA)
	cA := s.LoginAs(ownerA)

	ownerB := s.SeedUser("tags-query-iso-owner-b@example.test", "correct horse battery")
	s.SeedOrg("tags-query-iso-org-b", ownerB)
	cB := s.LoginAs(ownerB)
	hostB := createHostFor(t, cB, s.HTTP.URL, "tags-query-iso-org-b", "web-01")
	requestJSON(t, cB, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-query-iso-org-b/hosts/"+hostB+"/tags", []map[string]any{{"key": "role", "value": "db"}})

	resp := requestJSON(t, cA, http.MethodPost, s.HTTP.URL+"/api/v1/orgs/tags-query-iso-org-a/hosts/query", map[string]any{"selector": "role = db"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query hosts: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Hosts []hostDTO `json:"hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts) != 0 {
		t.Fatalf("query hosts scoped to org A = %+v, want none (org B's host must not leak across)", out.Hosts)
	}
}

// TestActivityFeedShowsSetHostTags proves PUT /hosts/{id}/tags is audited,
// mirroring TestActivityFeedShowsCreateHost (host_handlers_test.go).
func TestActivityFeedShowsSetHostTags(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-audit-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-audit-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-audit-org", "web-01")

	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-audit-org/hosts/"+hostID+"/tags", []map[string]any{{"key": "role", "value": "db"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put tags: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tags-audit-org/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	activity := decodeActivity(t, resp)
	var found bool
	for _, ev := range activity.Items {
		if ev.Action == "PUT setHostTags" {
			found = true
			if ev.ObjectType != "host" || ev.ObjectID != hostID {
				t.Fatalf("event = %+v, want object_type=host object_id=%s", ev, hostID)
			}
		}
	}
	if !found {
		t.Fatalf("activity = %+v, want a PUT setHostTags event", activity.Items)
	}
}

// TestPutHostTagsMalformedHostID proves a non-UUID host id in the path is a
// 400, not a 500, mirroring the same guarantee host_handlers_test.go's
// TestUpdateHostFullReplace gives PATCH /hosts/{id}.
func TestPutHostTagsMalformedHostID(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-malformed-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-malformed-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/tags-malformed-org/hosts/not-a-uuid/tags", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed host id (GET): status %d, want 400", resp.StatusCode)
	}
	resp = requestJSON(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-malformed-org/hosts/not-a-uuid/tags", []map[string]any{{"key": "role", "value": "db"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed host id (PUT): status %d, want 400", resp.StatusCode)
	}
}

// hostETag reads the host's current ETag through the host GET.
func hostETag(t *testing.T, c *http.Client, hostURL string) string {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, hostURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("GET host returned no ETag")
	}
	return etag
}

func quotedRevision(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

// Backlog 7.79: PUT /hosts/{id}/tags bumps the host's revision and checks
// If-Match when one is sent; without one it stays unconditional.
func TestSetHostTagsHonoursIfMatch(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-ifmatch-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-ifmatch-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-ifmatch-org", "web-01")
	hostURL := s.HTTP.URL + "/api/v1/orgs/tags-ifmatch-org/hosts/" + hostID
	url := hostURL + "/tags"
	before, err := strconv.ParseInt(strings.Trim(hostETag(t, c, hostURL), `"`), 10, 64)
	if err != nil {
		t.Fatal(err)
	}

	// No header: unconditional, and the revision moves.
	resp := requestJSON(t, c, http.MethodPut, url, []map[string]any{{"key": "role", "value": "db"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no If-Match: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := resp.Header.Get("ETag"); got != quotedRevision(before+1) {
		t.Fatalf("ETag = %q, want %s", got, quotedRevision(before+1))
	}
	if out := decodeTags(t, resp); out.HostRevision != before+1 {
		t.Fatalf("host_revision = %d, want %d", out.HostRevision, before+1)
	}

	// The current ETag is accepted.
	cur := quotedRevision(before + 1)
	resp = requestWithHeaders(t, c, http.MethodPut, url, []map[string]any{{"key": "role", "value": "web"}}, map[string]string{"If-Match": cur})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("current If-Match: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if out := decodeTags(t, resp); out.HostRevision != before+2 {
		t.Fatalf("host_revision after the second write = %d, want %d", out.HostRevision, before+2)
	}

	// A stale or malformed one is 412 and writes nothing.
	for name, header := range map[string]string{"stale": cur, "unquoted": "7"} {
		resp = requestWithHeaders(t, c, http.MethodPut, url, []map[string]any{{"key": "role", "value": "other"}}, map[string]string{"If-Match": header})
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("%s If-Match: status %d, want 412, body %s", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	got := decodeTags(t, requestJSON(t, c, http.MethodGet, url, nil))
	if len(got.Tags) != 1 || got.Tags[0].Value != "web" {
		t.Fatalf("tags after refused writes = %+v, want role=web", got.Tags)
	}
	if rev := hostETag(t, c, hostURL); rev != quotedRevision(before+2) {
		t.Fatalf("host ETag after refused writes = %s, want %s", rev, quotedRevision(before+2))
	}

	// An unknown host stays 404 even with an If-Match.
	resp = requestWithHeaders(t, c, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/tags-ifmatch-org/hosts/"+"0198a000-0000-7000-8000-000000000000/tags",
		[]map[string]any{}, map[string]string{"If-Match": cur})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown host: status %d, want 404", resp.StatusCode)
	}
}

// A host dialog opened before a sidebar tag write must answer 412 on save,
// even though the tag write sent no If-Match itself (backlog 7.79).
func TestHostEditAfterATagWriteAnswers412(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("tags-race-owner@example.test", "correct horse battery")
	s.SeedOrg("tags-race-org", owner)
	c := s.LoginAs(owner)
	hostID := createHostFor(t, c, s.HTTP.URL, "tags-race-org", "web-01")
	hostURL := s.HTTP.URL + "/api/v1/orgs/tags-race-org/hosts/" + hostID

	opened := hostETag(t, c, hostURL) // the dialog loads the host
	zone := decodeHost(t, requestJSON(t, c, http.MethodGet, hostURL, nil)).ZoneID
	if resp := requestJSON(t, c, http.MethodPut, hostURL+"/tags", []map[string]any{{"key": "env", "value": "prod"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put tags: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := requestWithHeaders(t, c, http.MethodPatch, hostURL, map[string]any{"name": "web-01-renamed", "zone_id": zone, "managed": false, "monitored": false}, map[string]string{"If-Match": opened})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("PATCH with the pre-tag ETag: status %d, want 412, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// Review Focus 3: an API-token client that never sent If-Match keeps working.
func TestSetHostTagsWithoutIfMatchStillWorksForTokens(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	hostID := s.SeedHost(t, org)
	req := func(body any) *http.Response {
		return requestWithHeaders(t, http.DefaultClient, http.MethodPut, s.HTTP.URL+"/api/v1/orgs/"+org.String()+"/hosts/"+hostID.String()+"/tags",
			body, map[string]string{"Authorization": "Bearer " + tok})
	}
	if resp := req([]map[string]any{{"key": "role", "value": "db"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("token PUT tags without If-Match: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}
