package api_test

import (
	"context"
	"encoding/json"
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

// TestBearerTokenAuthenticatesMe is the API-level counterpart to
// auth.TestBearerMiddleware: a real token minted through the service (not
// mounted behind any route yet — see token_handlers.go's doc comment on why
// the org-scoped CRUD routes stay unmounted until Task 10) authenticates a
// completely cookie-less request against an already-mounted route, and
// counts as MFA-complete the way Tokens.Resolve documents.
//
// This is deliberately not the brief's original handler test shape (owner
// creates the token through POST /orgs/{org}/tokens): that route needs
// Task 10's org authorization to exist safely, so it is written but left
// unmounted (see registerTokenRoutes) rather than exposed without it. What
// this test can and does exercise for real, end to end, through the actual
// running server: BearerMiddleware resolving a token minted by the service,
// an invalid bearer being rejected, and a revoked token being rejected.
func TestBearerTokenAuthenticatesMe(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("bearer@example.test", "correct horse battery")
	org := s.SeedOrg("bearer-org", uid)

	plain, tok, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "operator", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequest(t, s, "/api/v1/auth/me", plain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me with a valid bearer token: status %d, want 200", resp.StatusCode)
	}
	var body struct {
		MFAComplete bool `json:"mfa_complete"`
		User        struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.MFAComplete {
		t.Fatal("bearer-authenticated /me reported mfa_complete = false")
	}
	if body.User.Email != "bearer@example.test" {
		t.Fatalf("me email = %q, want bearer@example.test", body.User.Email)
	}

	if resp := bearerRequest(t, s, "/api/v1/auth/me", "pxt_not-a-real-token"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with an invalid bearer token: status %d, want 401", resp.StatusCode)
	}

	if err := s.Deps.Tokens.Revoke(context.Background(), org, tok.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if resp := bearerRequest(t, s, "/api/v1/auth/me", plain); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with a revoked bearer token: status %d, want 401", resp.StatusCode)
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
