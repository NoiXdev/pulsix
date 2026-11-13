package api_test

import (
	"net/http"
	"testing"
)

// TestHostKindThroughTheAPI (backlog 3.57): a host is created as a server
// unless the body says client; reads and lists carry the kind; a PATCH
// without kind keeps it, one with kind changes it; an unknown kind is a
// validation error (422) and writes nothing.
func TestHostKindThroughTheAPI(t *testing.T) {
	f := newSSHFixture(t, "hostkind-api-org")

	// The fixture's host was created without a kind.
	srv := decodeHost(t, requestJSON(t, f.c, http.MethodGet, f.org+"/hosts/"+f.hostID, nil))
	if srv.Kind != "server" {
		t.Fatalf("host created without kind = %q, want server", srv.Kind)
	}

	resp := postJSON(t, f.c, f.org+"/hosts", map[string]any{"name": "laptop-1", "zone_id": f.zoneID, "monitored": true, "kind": "client"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create client: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	client := decodeHost(t, resp)
	if client.Kind != "client" {
		t.Fatalf("created client = %q", client.Kind)
	}
	if got := decodeHost(t, requestJSON(t, f.c, http.MethodGet, f.org+"/hosts/"+client.ID, nil)); got.Kind != "client" {
		t.Fatalf("GET client = %q", got.Kind)
	}
	var list struct {
		Hosts []hostDTO `json:"hosts"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.org+"/hosts", nil), &list)
	kinds := map[string]string{}
	for _, h := range list.Hosts {
		kinds[h.Name] = h.Kind
	}
	if kinds["laptop-1"] != "client" || kinds["web-01"] != "server" {
		t.Fatalf("listed kinds = %v", kinds)
	}

	// Unknown kinds are refused on create and update, and change nothing.
	if resp := postJSON(t, f.c, f.org+"/hosts", map[string]any{"name": "laptop-2", "zone_id": f.zoneID, "kind": "laptop"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("create kind laptop: %d %s, want 422", resp.StatusCode, mustReadBody(t, resp))
	}
	url := f.org + "/hosts/" + client.ID
	etag := requestJSON(t, f.c, http.MethodGet, url, nil).Header.Get("ETag")
	if resp := requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "laptop-1", "zone_id": f.zoneID, "kind": "Server"},
		map[string]string{"If-Match": etag}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("patch kind Server: %d %s, want 422", resp.StatusCode, mustReadBody(t, resp))
	}

	// PATCH without kind keeps it.
	resp = requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "laptop-1", "zone_id": f.zoneID, "monitored": true},
		map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch without kind: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	etag = resp.Header.Get("ETag")
	if got := decodeHost(t, resp); got.Kind != "client" {
		t.Fatalf("after patch without kind = %q, want client", got.Kind)
	}

	// PATCH with kind changes it, under the revision.
	resp = requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "laptop-1", "zone_id": f.zoneID, "monitored": true, "kind": "server"},
		map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch to server: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := decodeHost(t, resp); got.Kind != "server" {
		t.Fatalf("after patch to server = %q", got.Kind)
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "laptop-1", "zone_id": f.zoneID, "kind": "client"},
		map[string]string{"If-Match": etag}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale patch: %d %s, want 412", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := decodeHost(t, requestJSON(t, f.c, http.MethodGet, url, nil)); got.Kind != "server" {
		t.Fatalf("after a stale patch = %q, want server", got.Kind)
	}
}

// TestHostQueryCarriesKind (backlog 3.57): POST /hosts/query reads hosts
// through the hand-written selector query, which must carry the kind like
// every other host read (the hosts list's selector mode shows it).
func TestHostQueryCarriesKind(t *testing.T) {
	f := newSSHFixture(t, "hostkind-query-api-org")

	for _, h := range []map[string]any{
		{"name": "laptop-q", "zone_id": f.zoneID, "kind": "client", "tags": []map[string]string{{"key": "role", "value": "kindq"}}},
		{"name": "server-q", "zone_id": f.zoneID, "tags": []map[string]string{{"key": "role", "value": "kindq"}}},
	} {
		if resp := postJSON(t, f.c, f.org+"/hosts", h); resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %v: %d %s", h["name"], resp.StatusCode, mustReadBody(t, resp))
		}
	}
	var list struct {
		Hosts []hostDTO `json:"hosts"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodPost, f.org+"/hosts/query", map[string]any{"selector": "role = kindq"}), &list)
	kinds := map[string]string{}
	for _, h := range list.Hosts {
		kinds[h.Name] = h.Kind
	}
	if len(kinds) != 2 || kinds["laptop-q"] != "client" || kinds["server-q"] != "server" {
		t.Fatalf("queried kinds = %v, want laptop-q=client server-q=server", kinds)
	}
}
