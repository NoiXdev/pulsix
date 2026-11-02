package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// newInteractiveSessionTestAPI mounts the account-security routes
// (passkeys + TOTP) on a bare chi+humachi router, the same white-box
// pattern newTokenTestAPI (token_handlers_test.go) uses for
// registerTokenRoutes: a principal is injected straight into the request
// context via DoCtx, standing in for whatever middleware chain would
// otherwise have produced it (CookieMiddleware for a browser session; not
// BearerMiddleware for a token, since fix round 1 stops mounting it at all
// on the real server — see api.go). deps.Passkeys, deps.TOTP and deps.Auth
// are left nil deliberately: every case this file exercises is rejected by
// requireInteractiveSession itself, before any of them would ever be
// dereferenced.
func newInteractiveSessionTestAPI(t *testing.T) humatest.TestAPI {
	t.Helper()
	r := chi.NewRouter()
	hapi := humachi.New(r, huma.DefaultConfig("interactive-session-test", "0.0.0"))
	registerPasskeyRoutes(hapi, Deps{})
	registerMFARoutes(hapi, Deps{})
	registerLocaleRoutes(hapi, Deps{})
	return humatest.Wrap(t, hapi)
}

// ctxWithTokenPrincipal builds a context carrying a bearer-token-shaped
// principal: MFAComplete true (Tokens.Resolve always sets this — see
// token.go) and TokenID set, standing in for exactly what BearerMiddleware
// would have put in the request context had it resolved a real
// "Authorization: Bearer pxt_…" header for a genuine, unrevoked,
// unexpired token, however narrowly the token itself is scoped.
func ctxWithTokenPrincipal(userID uuid.UUID) context.Context {
	tokenID := uuid.Must(uuid.NewV7())
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: userID, MFAComplete: true, TokenID: &tokenID})
}

// ctxWithPrincipal builds a context carrying a cookie-session-shaped
// principal (no TokenID): standing in for whatever CookieMiddleware would
// have put in the request context for a real session cookie.
func ctxWithPrincipal(userID uuid.UUID, mfaComplete bool) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: userID, MFAComplete: mfaComplete})
}

// doOptional calls DoCtx, omitting the body argument entirely when body is
// nil (a GET/DELETE with no body), since humatest.DoCtx treats a passed-in
// untyped nil differently from no variadic argument at all.
func doOptional(api humatest.TestAPI, ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	if body == nil {
		return api.DoCtx(ctx, method, path)
	}
	return api.DoCtx(ctx, method, path, body)
}

// TestInteractiveSessionRejectsTokenPrincipal is Finding 1's required
// regression test: every route that manages the account's own credentials
// — passkey register/finish/list/delete, TOTP enrol/confirm/delete — must
// reject a bearer-token-shaped principal with 403 via
// requireInteractiveSession, not accept it the way requireMFAComplete
// alone would (a token is always MFAComplete: true by construction, so
// before this fix a viewer-scoped token could register an
// attacker-controlled passkey or plant a TOTP secret on its owner's
// account and take it over completely).
//
// This intentionally does not also assert that a cookie session's request
// succeeds end to end here: deps.Passkeys/deps.TOTP are nil above (safe
// only because every case below is rejected before either is
// dereferenced), and exercising the success path needs real services.
// That positive proof — "a cookie session with a completed second factor
// still gets through" — already exists for the two routes the brief calls
// out by name: TestPasskeyBeginEndpoints (POST
// /auth/passkeys/register/begin, 200) and TestTOTPGatesTheSession (POST
// /auth/totp/enrol, 200), both in passkey_handlers_test.go/
// mfa_handlers_test.go, run against the real apitest server with a real
// MFA-complete cookie session (TokenID nil) — untouched by this fix and
// still green, since requireInteractiveSession only adds a check on top of
// requireMFAComplete's existing one.
func TestInteractiveSessionRejectsTokenPrincipal(t *testing.T) {
	api := newInteractiveSessionTestAPI(t)
	uid := uuid.Must(uuid.NewV7())
	tokenCtx := ctxWithTokenPrincipal(uid)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"passkey register begin", http.MethodPost, "/api/v1/auth/passkeys/register/begin", struct{}{}},
		{"passkey register finish", http.MethodPost, "/api/v1/auth/passkeys/register/finish", map[string]any{
			"ceremony_id": uuid.Must(uuid.NewV7()).String(), "name": "x", "response": map[string]any{},
		}},
		{"passkey list", http.MethodGet, "/api/v1/auth/passkeys", nil},
		{"passkey delete", http.MethodDelete, "/api/v1/auth/passkeys/" + uuid.Must(uuid.NewV7()).String(), nil},
		{"totp enrol", http.MethodPost, "/api/v1/auth/totp/enrol", struct{}{}},
		{"totp confirm", http.MethodPost, "/api/v1/auth/totp/confirm", map[string]any{"code": "000000"}},
		{"totp delete", http.MethodDelete, "/api/v1/auth/totp", map[string]any{"code": "000000"}},
		{"set own language", http.MethodPut, "/api/v1/auth/me/locale", map[string]any{"locale": "de"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doOptional(api, tokenCtx, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("token principal: status %d, want 403, body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestInteractiveSessionAllowsCookiePrincipal proves the flip side directly
// against requireInteractiveSession, without needing real Passkeys/TOTP
// services: a cookie-session-shaped principal (TokenID nil, MFAComplete
// true) is not itself what requireInteractiveSession rejects. It is a
// pure function of ctx, so it can be called directly rather than through a
// mounted route — this is the precise unit-level counterpart to
// TestInteractiveSessionRejectsTokenPrincipal's route-level 403 assertions
// above.
func TestInteractiveSessionAllowsCookiePrincipal(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	p, err := requireInteractiveSession(ctxWithPrincipal(uid, true))
	if err != nil {
		t.Fatalf("cookie-session principal rejected: %v", err)
	}
	if p.UserID != uid || p.TokenID != nil {
		t.Fatalf("principal = %+v, want UserID %v and TokenID nil", p, uid)
	}
}

// TestRequireInteractiveSession covers requireInteractiveSession's own
// branches directly: no principal at all (401, from requireSession), a
// principal that has not completed a second factor (403, from
// requireMFAComplete — same as before this fix), and a token principal
// that has (403, the new check this fix adds).
func TestRequireInteractiveSession(t *testing.T) {
	t.Run("no principal", func(t *testing.T) {
		if _, err := requireInteractiveSession(context.Background()); err == nil {
			t.Fatal("want an error for an anonymous context")
		}
	})
	t.Run("MFA-pending session", func(t *testing.T) {
		if _, err := requireInteractiveSession(ctxWithPrincipal(uuid.Must(uuid.NewV7()), false)); err == nil {
			t.Fatal("want an error for a session that has not completed MFA")
		}
	})
	t.Run("token principal", func(t *testing.T) {
		if _, err := requireInteractiveSession(ctxWithTokenPrincipal(uuid.Must(uuid.NewV7()))); err == nil {
			t.Fatal("want an error for a bearer-token-shaped principal")
		}
	})
}
