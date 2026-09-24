package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// headersOf runs one request through the middleware and returns the
// response headers the browser would see.
func headersOf(t *testing.T, path string) http.Header {
	t.Helper()
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Header()
}

// Every response carries the headers that cost nothing and close off whole
// classes of attack: no framing, no MIME sniffing, no referrer leaking the
// path of an authenticated page to a third party.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	for _, path := range []string{"/", "/login", "/api/v1/orgs/acme/hosts", "/api/explorer", "/healthz", "/install.sh"} {
		t.Run(path, func(t *testing.T) {
			h := headersOf(t, path)
			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "no-referrer",
			} {
				if got := h.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if got := h.Get("Permissions-Policy"); got == "" {
				t.Error("Permissions-Policy is missing")
			}
			csp := h.Get("Content-Security-Policy")
			for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'"} {
				if !strings.Contains(csp, want) {
					t.Errorf("CSP %q is missing %q", csp, want)
				}
			}
		})
	}
}

// The SPA and the API get the strict policy: the part of this application
// that handles an organization's data loads scripts from nowhere but here.
func TestSecurityHeadersStrictScriptPolicyOutsideTheExplorer(t *testing.T) {
	for _, path := range []string{"/", "/o/acme/hosts", "/api/v1/orgs/acme/hosts", "/assets/index-abc.js"} {
		csp := headersOf(t, path).Get("Content-Security-Policy")
		if strings.Contains(csp, "cdn.jsdelivr.net") {
			t.Errorf("%s: the explorer's third-party exception leaked into the strict policy: %s", path, csp)
		}
		if !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("%s: CSP %q must pin scripts to this origin", path, csp)
		}
	}
}

// The API explorer is the one page that loads a third-party viewer, so it
// is the one page whose policy names that origin — and nothing else does.
func TestSecurityHeadersExplorerException(t *testing.T) {
	csp := headersOf(t, "/api/explorer").Get("Content-Security-Policy")
	if !strings.Contains(csp, "https://cdn.jsdelivr.net") {
		t.Fatalf("the explorer's CSP must admit the viewer it loads: %s", csp)
	}
	// Even there, the loosening is scoped: no inline scripts, no eval.
	for _, forbidden := range []string{"'unsafe-inline' 'unsafe-eval'", "script-src 'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("the explorer's CSP must not carry %q: %s", forbidden, csp)
		}
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Error("the explorer must stay unframeable like everything else")
	}
}

// The exception is keyed on the exact path, not a prefix: a route that
// merely starts with the same characters must not inherit it.
func TestSecurityHeadersExplorerExceptionIsExact(t *testing.T) {
	for _, path := range []string{"/api/explorer/", "/api/explorer-evil", "/api/explorerx"} {
		if csp := headersOf(t, path).Get("Content-Security-Policy"); strings.Contains(csp, "cdn.jsdelivr.net") {
			t.Errorf("%s inherited the explorer exception: %s", path, csp)
		}
	}
}
