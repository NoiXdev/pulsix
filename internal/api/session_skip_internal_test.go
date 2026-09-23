package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSkipsSessionLookup pins the route set that skips the session lookup
// for backlog 1.22: every path outside the API subtree — the SPA routes and
// embedded assets web.Handler serves as the router's NotFound fallback,
// plus /healthz — skips, while every /api path (all handler operations live
// under /api/v1; the explorer and the OpenAPI document sit at /api/explorer
// and /api/openapi*) keeps the lookup the session principal is read from.
func TestSkipsSessionLookup(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// The SPA static handler's territory.
		{"/", true},
		{"/assets/index-9f2a1b3c.js", true},
		{"/assets/chunk-abc123.css", true},
		{"/robots.txt", true},
		{"/favicon.ico", true},
		{"/login", true},
		{"/o/acme/hosts", true},
		{"/totally-unknown", true},
		// Public and principal-free.
		{"/healthz", true},
		// API territory: the session principal is consulted here.
		{"/api", false},
		{"/api/v1/auth/me", false},
		{"/api/v1/does-not-exist", false},
		{"/api/explorer", false},
		{"/api/openapi.json", false},
		{"/api/openapi.json.gz", false},
	}
	for _, tc := range cases {
		if got := skipsSessionLookup(tc.path); got != tc.want {
			t.Errorf("skipsSessionLookup(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestStaticSessionSkipBypassesWrappedMiddleware proves the short-circuit
// at the middleware seam: for a skipped path the wrapped session middleware
// never runs (no lookup happens), the rest of the chain still does; for an
// API path the wrapped middleware runs exactly once, i.e. the middleware
// chain is unchanged for protected routes.
func TestStaticSessionSkipBypassesWrappedMiddleware(t *testing.T) {
	var lookups, nextCalled int
	sessionMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lookups++
			next.ServeHTTP(w, r)
		})
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled++
	})
	h := staticSessionSkip(sessionMW)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if lookups != 0 || nextCalled != 1 {
		t.Fatalf("static asset request: lookups = %d, next calls = %d, want 0/1 (skip without breaking the chain)", lookups, nextCalled)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	if lookups != 1 || nextCalled != 2 {
		t.Fatalf("protected API request: lookups = %d, next calls = %d, want 1/2 (lookup still happens)", lookups, nextCalled)
	}
}
