package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesIndexForAppRoutes covers the history-fallback contract:
// both the root path and a deep client-side route that has no matching
// file on disk must both serve the SPA's index.html, so a hard reload (or
// a shared link) on e.g. /o/acme/hosts lands the React Router app instead
// of a raw 404 from the file server.
func TestHandlerServesIndexForAppRoutes(t *testing.T) {
	h := Handler()

	for _, path := range []string{"/", "/o/acme/hosts", "/o/acme/hosts/some-host-id", "/login", "/setup"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != 200 {
				t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("GET %s: body does not look like the index page: %q", path, rec.Body.String())
			}
		})
	}
}

// TestHandlerAPIPathsAlways404 is the other half of the contract: a
// missing /api/... path must never fall back to index.html — that would
// turn a real API error (a typo'd path, a removed endpoint) into a
// misleading 200 carrying an HTML page instead of a 404, which the
// generated client and its hooks assume they will never see.
func TestHandlerAPIPathsAlways404(t *testing.T) {
	h := Handler()

	for _, path := range []string{"/api/v1/nothing", "/api/", "/api/v1/orgs/acme/hosts/does-not-exist-either"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != 404 {
				t.Fatalf("GET %s: status = %d, want 404", path, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("GET %s: 404 body looks like the SPA index page, not a real 404", path)
			}
		})
	}
}

// TestHandlerServesRealStaticAsset guards the other branch of Handler's
// logic — a path that does exist in the embedded filesystem must be served
// as itself, not silently rewritten to index.html. robots.txt (rather than
// index.html) is used deliberately: http.FileServer 301-redirects a direct
// request for the implicit index file to "/", which would make this test
// exercise the same redirect path as TestHandlerServesIndexForAppRoutes
// instead of the "file exists, serve it" branch it means to cover.
func TestHandlerServesRealStaticAsset(t *testing.T) {
	h := Handler()

	req := httptest.NewRequest("GET", "/robots.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("GET /robots.txt: status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /robots.txt: body looks like the SPA index page, not the static asset: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Disallow") {
		t.Errorf("GET /robots.txt: unexpected body: %q", rec.Body.String())
	}
}
