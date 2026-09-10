package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// bearerRequest issues a GET against path with an Authorization: Bearer
// header, using a jar-less client — a bearer credential must not depend on
// cookies at all.
func bearerRequest(t *testing.T, s *apitest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.HTTP.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestBearerTokenNotYetResolvedOnRealServer documents and guards the
// current, deliberate state (Finding 1, fix round 1): auth.BearerMiddleware
// is fully implemented and exercised at the unit level
// (auth.TestBearerMiddleware), but api.New no longer mounts it on this
// package's router — see api.go's comment on why: nothing here consults
// TokenScopeFromContext yet (registerTokenRoutes, the one consumer, stays
// unmounted until Task 10 adds the org-authorization it needs), so
// resolving a bearer credential into every other route's context would
// only be attack surface with no matching benefit. A real, valid,
// unrevoked token therefore authenticates nothing on the actually-running
// server today: the Authorization header is never inspected, so the
// request is treated exactly like one with no credential at all (401, the
// same as TestBearerTokenCannotAuthenticateWithoutHeader below).
//
// This guards the deferral the way TestOrgTokenRoutesNotYetMounted guards
// registerTokenRoutes staying unmounted: if BearerMiddleware is mounted
// again without Task 10's org-scoped authorization in front of it, this
// test starts failing (401 stops meaning "the header was never resolved")
// as a signal to revisit the decision.
func TestBearerTokenNotYetResolvedOnRealServer(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("bearer@example.test", "correct horse battery")
	org := s.SeedOrg("bearer-org", uid)

	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "operator", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequest(t, s, "/api/v1/auth/me", plain)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with a valid-but-unresolved bearer token: status %d, want 401 (BearerMiddleware is not mounted on the real server yet)", resp.StatusCode)
	}
}

// TestOrgTokenRoutesNotYetMounted documents and guards the current,
// deliberate state: registerTokenRoutes (token_handlers.go) exists and is
// tested directly (token_handlers_test.go), but registerRoutes in api.go
// does not call it, because these three routes need Task 10's
// org-authorization layer in front of them (see registerTokenRoutes' doc
// comment) — they check only requireMFAComplete today, with no
// org-membership check at all. If a future change mounts them without also
// adding that authorization, this test starts failing (404 turns into
// something else) as a signal to revisit this decision, not because the
// routes are expected to stay unmounted forever.
func TestOrgTokenRoutesNotYetMounted(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("route-guard@example.test", "correct horse battery")
	org := s.SeedOrg("route-guard-org", uid)
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "ci", "role": "operator"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST org tokens: status %d, want 404 (route not mounted until Task 10)", resp.StatusCode)
	}
}

// TestBearerTokenCannotAuthenticateWithoutHeader asserts that omitting the
// Authorization header (rather than sending an invalid one) still leaves
// the request anonymous, not authenticated — the counterpart to
// auth.TestBearerMiddleware's "no header" case, exercised through the real
// mounted route.
func TestBearerTokenCannotAuthenticateWithoutHeader(t *testing.T) {
	s := apitest.New(t)
	if resp := bearerRequest(t, s, "/api/v1/auth/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with no credential at all: status %d, want 401", resp.StatusCode)
	}
}
