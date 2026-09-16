package api_test

import (
	"net/http"
	"testing"
)

// badIDCase is one write route's cross-org and malformed-id check (backlog
// 2.19). url builds the request URL for a given id string — the route's
// one variable id slot; a route with two id path parameters (inventory
// group membership's group and host ids) gets one badIDCase per slot, the
// other slot held at a real, own-organization value. body and headers are
// otherwise-valid for the route (a well-formed request that would succeed
// against the caller's own resources), so the only thing under test is the
// id itself.
type badIDCase struct {
	name      string
	method    string
	url       func(id string) string
	body      any
	headers   map[string]string
	foreignID string // a syntactically valid id belonging to a different organization
}

// assertRejectsBadIDs runs both of 2.19's checks against tc:
//   - a syntactically valid id belonging to a different organization must
//     404 — the resource is invisible to this organization, not merely
//     forbidden; a 403 or anything else would leak that it exists.
//   - a malformed id (not a UUID at all) must 400 or 404, and must never,
//     under any circumstance, 500 — a write route's id parsing runs before
//     it touches the store, so a bad id must never reach a query, let
//     alone crash one.
func assertRejectsBadIDs(t *testing.T, c *http.Client, tc badIDCase) {
	t.Helper()
	t.Run(tc.name+"/cross-org id", func(t *testing.T) {
		resp := requestWithHeaders(t, c, tc.method, tc.url(tc.foreignID), tc.body, tc.headers)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d, want 404: %s", resp.StatusCode, mustReadBody(t, resp))
		}
	})
	t.Run(tc.name+"/malformed id", func(t *testing.T) {
		resp := requestWithHeaders(t, c, tc.method, tc.url("not-a-uuid"), tc.body, tc.headers)
		if resp.StatusCode >= http.StatusInternalServerError {
			t.Fatalf("malformed id must never 500, got %d: %s", resp.StatusCode, mustReadBody(t, resp))
		}
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d, want 400 or 404 (never 500): %s", resp.StatusCode, mustReadBody(t, resp))
		}
	})
}
