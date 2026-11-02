package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
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

// bearerRequestJSON is bearerRequest plus a JSON body, for the bearer-token
// tests that need to PATCH/POST a payload (e.g. member_handlers_test.go's
// role-cap regression test) rather than just GET/DELETE.
func bearerRequestJSON(t *testing.T, s *apitest.Server, method, path, token string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, s.HTTP.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

// TestBearerTokenCannotCreateTokens covers "credentials do not mint
// credentials": even an owner-role token for the very organization in the
// path gets 403 from token creation, and nothing is minted — while the same
// token can still list its organization's tokens.
func TestBearerTokenCannotCreateTokens(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("mint@example.test", "correct horse battery")
	org := s.SeedOrg("mint-org", uid)
	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "owner", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequestJSON(t, s, http.MethodPost, "/api/v1/orgs/mint-org/tokens", plain,
		map[string]any{"name": "successor", "role": "viewer"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token creation with a bearer token: status %d, want 403", resp.StatusCode)
	}
	toks, err := s.Deps.Tokens.List(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 1 {
		t.Fatalf("org has %d tokens after a refused creation, want only the original", len(toks))
	}

	resp = bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/mint-org/tokens", plain)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token list with a bearer token: status %d, want 200", resp.StatusCode)
	}
}

// TestBearerRequestsTouchLastUsedAtMostOncePerMinute drives backlog 1.22's
// throttle end to end through the real HTTP stack: two rapid bearer requests
// with the same token each resolve through BearerMiddleware → Tokens.Resolve
// → TouchLastUsed, but only the first may write api_tokens.last_used_at —
// the SQL predicate in TouchAPIToken (the source of truth) suppresses the
// second write inside the 60 s window, so the row the two requests leave
// behind must be identical. The row itself is the assertion: the test reads
// last_used_at through the raw pool after each request and requires the
// second request to leave it unchanged.
func TestBearerRequestsTouchLastUsedAtMostOncePerMinute(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("touch-throttle@example.test", "correct horse battery")
	org := s.SeedOrg("touch-throttle-org", uid)
	plain, tok, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "admin", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/touch-throttle-org/members", plain)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first members request with a valid bearer token: status %d, want 200", resp.StatusCode)
	}

	ctx := context.Background()
	var first pgtype.Timestamptz
	if err := s.DB.Pool().QueryRow(ctx, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if !first.Valid {
		t.Fatal("first bearer request did not record last_used_at")
	}

	resp = bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/touch-throttle-org/members", plain)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second members request with a valid bearer token: status %d, want 200", resp.StatusCode)
	}

	var second pgtype.Timestamptz
	if err := s.DB.Pool().QueryRow(ctx, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if !second.Time.Equal(first.Time) {
		t.Fatalf("second rapid request moved last_used_at %v -> %v, want one write per minute", first.Time, second.Time)
	}
}

// TestBearerTokenCannotManageMembers is backlog 7.31: a token may not mint
// a token, and a membership is the same kind of successor credential. An
// owner-scoped token used to be able to invite an address of its holder's
// and accept it as admin, a membership its own revocation never touched.
// Inviting, changing a role and removing a member now need a person at an
// interactive session, like creating a token.
func TestBearerTokenCannotManageMembers(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("members-token@example.test", "correct horse battery")
	org := s.SeedOrg("members-token-org", uid)
	member := s.SeedUser("members-token-member@example.test", "correct horse battery")
	s.AddMember(org, member, tenancy.RoleViewer)
	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", "owner", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, c := range []struct {
		name, method, path string
		body               any
	}{
		{"invite", http.MethodPost, "/api/v1/orgs/members-token-org/invites", map[string]any{"email": "successor@example.test", "role": "admin"}},
		{"change role", http.MethodPatch, "/api/v1/orgs/members-token-org/members/" + member.String(), map[string]any{"role": "admin"}},
		{"remove", http.MethodDelete, "/api/v1/orgs/members-token-org/members/" + member.String(), nil},
	} {
		resp := bearerRequestJSON(t, s, c.method, c.path, plain, c.body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a bearer token: status %d, want 403", c.name, resp.StatusCode)
		}
	}
	if sent := s.Mail.Sent(); len(sent) != 0 {
		t.Fatalf("a refused invitation was mailed: %+v", sent)
	}

	resp := bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/members-token-org/members", plain)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member list with a bearer token: status %d, want 200", resp.StatusCode)
	}
}
