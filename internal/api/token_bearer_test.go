package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// bearerRequest issues a request against path with an Authorization: Bearer
// header, using a jar-less client — a bearer credential must not depend on
// cookies at all.
func bearerRequest(t *testing.T, s *apitest.Server, method, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, s.HTTP.URL+path, nil)
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

// TestBearerTokenAuthenticatesOrgScopedRoute is the carry-over Task 10
// completes: Task 9 built auth.BearerMiddleware and the org-scoped token
// routes but mounted neither, because mounting the middleware on the root
// router would have let any token satisfy the account-security gates too
// (see api.go's comment on New). This proves the actually-correct mount —
// a *huma.Group scoped to the org-scoped routes under /api/v1/orgs/... —
// works end to end: a real, valid, unrevoked bearer token authenticates
// against GET /api/v1/orgs/{org}/members with no cookie at all.
func TestBearerTokenAuthenticatesOrgScopedRoute(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("bearer@example.test", "correct horse battery")
	org := s.SeedOrg("bearer-org", uid)

	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "admin", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/bearer-org/members", plain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("members with a valid bearer token: status %d, want 200", resp.StatusCode)
	}
}

// TestBearerTokenCannotReachPasskeyRegistration is the second carry-over
// test: a bearer token, however privileged, must never reach an
// account-security route with no org in its path.
// requireInteractiveSession (auth_handlers.go) rejects any principal with
// TokenID set outright, and — separately — POST /auth/passkeys/register/begin
// sits entirely outside the bearer-enabled *huma.Group (org.go/api.go only
// mount BearerMiddleware on the org-scoped subtree), so the token's
// Authorization header is never even resolved for this route: it behaves
// exactly like a request with no credential at all.
func TestBearerTokenCannotReachPasskeyRegistration(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("bearer-passkey@example.test", "correct horse battery")
	org := s.SeedOrg("bearer-passkey-org", uid)

	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "owner", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequest(t, s, http.MethodPost, "/api/v1/auth/passkeys/register/begin", plain)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("passkey register/begin with a bearer token: status %d, want 401 (no principal resolved outside the org-scoped group)", resp.StatusCode)
	}
}

// TestBearerTokenCannotAuthenticateWithoutHeader asserts that omitting the
// Authorization header (rather than sending an invalid one) still leaves
// the request anonymous, not authenticated — the counterpart to
// auth.TestBearerMiddleware's "no header" case, exercised through the real
// mounted route.
func TestBearerTokenCannotAuthenticateWithoutHeader(t *testing.T) {
	s := apitest.New(t)
	if resp := bearerRequest(t, s, http.MethodGet, "/api/v1/auth/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with no credential at all: status %d, want 401", resp.StatusCode)
	}
}

// TestBearerTokenScopedToWrongOrgIsForbidden proves requireOrg's own
// isolation guarantee end to end: a token minted for one org is rejected —
// not merely "sees no data" — when presented against a different org.
func TestBearerTokenScopedToWrongOrgIsForbidden(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("bearer-scope@example.test", "correct horse battery")
	orgA := s.SeedOrg("bearer-scope-a", uid)
	_ = s.SeedOrg("bearer-scope-b", uid)

	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, orgA, "ci", "owner", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/bearer-scope-b/members", plain)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("members via a token scoped to a different org: status %d, want 403", resp.StatusCode)
	}
}
