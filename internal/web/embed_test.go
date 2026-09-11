package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// testFS is a small fixture standing in for a real build's output. Using
// fstest.MapFS instead of the tracked placeholder files under dist/ means
// these tests describe their own expectations instead of depending on
// whatever happens to be on disk — `make build` replaces those tracked
// files with the real frontend build (and CI never commits the result
// back), so a test that opened them directly would break the moment
// someone ran `make build` locally before `go test` (finding "Important
// 10", task-15a-report.md fix round 1).
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<html><body>pulsix</body></html>")},
		"robots.txt":           &fstest.MapFile{Data: []byte("User-agent: *\nDisallow:\n")},
		"assets/app.abc123.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
}

// TestHandlerServesIndexForAppRoutes covers the history-fallback contract:
// both the root path and a deep, extension-less client-side route that has
// no matching file on disk serve the SPA's index.html, so a hard reload
// (or a shared link) on e.g. /o/acme/hosts lands the React app instead of
// a raw 404 from the file server.
func TestHandlerServesIndexForAppRoutes(t *testing.T) {
	h := HandlerFS(testFS())

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
// missing /api/... path — with or without a trailing slash — must never
// fall back to index.html, and bare "/api" must 404 too.
func TestHandlerAPIPathsAlways404(t *testing.T) {
	h := HandlerFS(testFS())

	for _, path := range []string{"/api", "/api/", "/api/v1/nothing", "/api/v1/orgs/acme/hosts/does-not-exist-either"} {
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
// logic — a path that does exist in the fs must be served as itself, not
// silently rewritten to index.html.
func TestHandlerServesRealStaticAsset(t *testing.T) {
	h := HandlerFS(testFS())

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

// TestHandlerMissingHashedAssetIs404 is finding "Important 9": after a
// redeploy, a browser holding a stale index.html may request an
// /assets/... chunk that no longer exists. That must 404, never serve
// index.html — a 200 carrying HTML in place of a JS/CSS chunk white-screens
// the app instead of prompting a reload.
func TestHandlerMissingHashedAssetIs404(t *testing.T) {
	h := HandlerFS(testFS())

	for _, path := range []string{"/assets/app.def456.js", "/assets/missing", "/favicon.ico"} {
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

// TestHandlerAssetsDirectoryDoes404NotList is the other half of finding
// "Important 9": requesting the /assets/ directory itself must not
// produce a directory listing.
func TestHandlerAssetsDirectoryDoes404NotList(t *testing.T) {
	h := HandlerFS(testFS())

	req := httptest.NewRequest("GET", "/assets/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("GET /assets/: status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "app.abc123.js") || strings.Contains(strings.ToLower(body), "index of") {
		t.Errorf("GET /assets/: response looks like a directory listing: %q", body)
	}
}

// TestHandlerServesRealAsset confirms an asset that does exist is served
// as itself even under /assets/, where a stricter 404-on-miss rule now
// applies.
func TestHandlerServesRealAsset(t *testing.T) {
	h := HandlerFS(testFS())

	req := httptest.NewRequest("GET", "/assets/app.abc123.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("GET /assets/app.abc123.js: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("GET /assets/app.abc123.js: unexpected body: %q", rec.Body.String())
	}
}

// TestHandlerProductionFSEmbeds is a light smoke test that the real
// embedded dist/ tree (Handler's default, via FS()) still wires up
// correctly — the fixture-based tests above cover the routing logic in
// detail.
func TestHandlerProductionFSEmbeds(t *testing.T) {
	h := Handler()

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}
}
