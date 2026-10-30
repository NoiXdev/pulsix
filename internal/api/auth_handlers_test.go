package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
)

func postJSON(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLoginLogoutFlow(t *testing.T) {
	s := apitest.New(t)
	s.SeedUser("tim@example.test", "correct horse battery")
	c := s.Client()

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "tim@example.test", "password": "wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d", resp.StatusCode)
	}
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "tim@example.test", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	me, err := c.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil || me.StatusCode != http.StatusOK {
		t.Fatalf("me: %v %d", err, me.StatusCode)
	}
	var body struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	_ = json.NewDecoder(me.Body).Decode(&body)
	if body.User.Email != "tim@example.test" {
		t.Fatalf("me email = %q", body.User.Email)
	}
	postJSON(t, c, s.HTTP.URL+"/api/v1/auth/logout", struct{}{})
	me, _ = c.Get(s.HTTP.URL + "/api/v1/auth/me")
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: status %d", me.StatusCode)
	}
}

// TestLoginCookieAttributes asserts HttpOnly/SameSite/Path/MaxAge on the
// real Set-Cookie response of POST /api/v1/auth/login, rather than only on
// the auth.SetCookie/ClearCookie helpers that Finding 3 found dead and
// removed — auth_handlers.go builds its own http.Cookie in sessionCookie,
// and that is the only code that ever actually runs in production.
func TestLoginCookieAttributes(t *testing.T) {
	s := apitest.New(t)
	s.SeedUser("cookie@example.test", "correct horse battery")

	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "cookie@example.test", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	cookie := findCookie(t, resp, auth.CookieName)
	if !cookie.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("MaxAge = %d, want > 0", cookie.MaxAge)
	}
	if cookie.Secure {
		t.Error("cookie is Secure with Deps.Secure unset")
	}
}

// TestLoginCookieSecureWhenConfigured covers the Deps.Secure = true case,
// which needs apitest.WithSecureCookies to build the test server with it
// set (Finding 3).
func TestLoginCookieSecureWhenConfigured(t *testing.T) {
	s := apitest.New(t, apitest.WithSecureCookies())
	s.SeedUser("securecookie@example.test", "correct horse battery")

	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "securecookie@example.test", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	cookie := findCookie(t, resp, auth.CookieName)
	if !cookie.Secure {
		t.Error("cookie is not Secure with Deps.Secure = true")
	}
}

func findCookie(t *testing.T, resp *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q cookie in response", name)
	return nil
}

// authWithFailingLogout wraps a real api.AuthService and forces Logout to
// fail, something the real service backed by a healthy test database has
// no easy way to do on its own.
type authWithFailingLogout struct {
	api.AuthService
	err error
}

func (f authWithFailingLogout) Logout(context.Context, auth.Principal) error { return f.err }

// TestLogoutRevokeFailureDoesNotReport200 covers Finding 1: if revocation
// fails, the handler must not report success (200 plus a Set-Cookie that
// clears the cookie) — an already-exfiltrated cookie must not be told it
// survived a logout the user believes succeeded.
func TestLogoutRevokeFailureDoesNotReport200(t *testing.T) {
	s := apitest.New(t, apitest.WithAuthWrapper(func(a api.AuthService) api.AuthService {
		return authWithFailingLogout{AuthService: a, err: errors.New("revoke failed: connection reset by peer")}
	}))
	userID := s.SeedUser("logoutfail@example.test", "correct horse battery")
	c := s.LoginAs(userID)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/logout", struct{}{})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("logout reported 200 despite a failed revoke")
	}
}

// authWithFailingLogin wraps a real api.AuthService and forces Login to
// fail with an arbitrary, unmapped error.
type authWithFailingLogin struct {
	api.AuthService
	err error
}

func (f authWithFailingLogin) Login(context.Context, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, f.err
}

// TestUnmappedAuthErrorDoesNotLeakDetail covers Finding 6: an error that
// mapAuthErr doesn't recognize must become a generic message, with the
// original detail (a raw driver error, a hash-parsing failure, ...) logged
// server-side instead of serialized into the response body.
func TestUnmappedAuthErrorDoesNotLeakDetail(t *testing.T) {
	secret := "dial tcp 10.0.0.5:5432: connect: connection refused"
	s := apitest.New(t, apitest.WithAuthWrapper(func(a api.AuthService) api.AuthService {
		return authWithFailingLogin{AuthService: a, err: errors.New(secret)}
	}))

	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "a@example.test", "password": "whatever"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), "10.0.0.5") {
		t.Fatalf("response body leaked internal detail: %s", buf.String())
	}
}

// TestExplorerServed covers Finding 7: the explorer page and the OpenAPI
// spec it loads must require a session — both sit on the same origin as
// the session cookie and, unauthenticated, hand any caller the full shape
// of the API plus a working request console.
func TestExplorerServed(t *testing.T) {
	s := apitest.New(t)

	resp, err := http.Get(s.HTTP.URL + "/api/explorer")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("explorer without a session: %v %d, want 401", err, resp.StatusCode)
	}
	spec, err := http.Get(s.HTTP.URL + "/api/openapi.json")
	if err != nil || spec.StatusCode != http.StatusUnauthorized {
		t.Fatalf("openapi without a session: %v %d, want 401", err, spec.StatusCode)
	}

	userID := s.SeedUser("explorer@example.test", "correct horse battery")
	c := s.LoginAs(userID)

	resp, err = c.Get(s.HTTP.URL + "/api/explorer")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("explorer with a session: %v %d, want 200", err, resp.StatusCode)
	}
	spec, err = c.Get(s.HTTP.URL + "/api/openapi.json")
	if err != nil || spec.StatusCode != http.StatusOK {
		t.Fatalf("openapi with a session: %v %d, want 200", err, spec.StatusCode)
	}
}

// TestSchemasRouteIsGatedLikeTheSpec is backlog 7.15: huma's per-type JSON
// Schema route describes the same API as the OpenAPI document, so it sits
// behind the same MFA gate. It used to stay at huma's default /schemas,
// outside /api, where neither the session nor the gate ever looked.
func TestSchemasRouteIsGatedLikeTheSpec(t *testing.T) {
	s := apitest.New(t)

	isSchema := func(resp *http.Response) bool {
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&body) == nil && body["properties"] != nil
	}
	for _, path := range []string{"/schemas/HostDTO.json", "/api/schemas/HostDTO.json"} {
		resp, err := http.Get(s.HTTP.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if isSchema(resp) {
			t.Fatalf("anonymous GET %s returned the type's JSON Schema", path)
		}
	}

	userID := s.SeedUser("schemas@example.test", "correct horse battery")
	c := s.LoginAs(userID)
	resp, err := c.Get(s.HTTP.URL + "/api/schemas/HostDTO.json")
	if err != nil || !isSchema(resp) {
		t.Fatalf("schema with an MFA-complete session: %v, want the JSON Schema", err)
	}
}

// TestRequireMFACompleteGatesExplorerNotMe covers Finding 8: a session that
// hasn't completed a required second factor must still reach /auth/me (so
// the SPA can discover the pending factor) but must be refused by the
// explorer gate, which uses requireMFAComplete rather than requireSession.
func TestRequireMFACompleteGatesExplorerNotMe(t *testing.T) {
	s := apitest.New(t)
	userID := s.SeedUser("pending@example.test", "correct horse battery")
	c := s.LoginAsPending(userID)

	me, err := c.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil || me.StatusCode != http.StatusOK {
		t.Fatalf("me (requireSession) with pending MFA: %v %d, want 200", err, me.StatusCode)
	}
	var body struct {
		MFAComplete bool `json:"mfa_complete"`
	}
	_ = json.NewDecoder(me.Body).Decode(&body)
	if body.MFAComplete {
		t.Fatal("me reported mfa_complete = true for a pending session")
	}

	resp, err := c.Get(s.HTTP.URL + "/api/explorer")
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("explorer (requireMFAComplete) with pending MFA: %v %d, want 403", err, resp.StatusCode)
	}
}
