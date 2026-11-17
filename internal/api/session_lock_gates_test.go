package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
)

const sessionLocked = "session_locked"

// lockedSession signs the user in with an MFA-complete session and locks it
// through the screenlock service, as the lock route will.
func lockedSession(t *testing.T, s *apitest.Server, uid uuid.UUID) (*http.Client, uuid.UUID) {
	t.Helper()
	sid, cookie, err := s.Sessions.Create(t.Context(), uid, true)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: cookie}})
	if err := s.Deps.ScreenLock.SetPIN(t.Context(), uid, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := s.Deps.ScreenLock.Lock(t.Context(), uid, sid); err != nil {
		t.Fatal(err)
	}
	return c, sid
}

func lockAdmin(t *testing.T, s *apitest.Server, email string) uuid.UUID {
	t.Helper()
	uid := s.SeedUser(email, "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET instance_admin = true WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	s.SeedPasskey(uid, "key")
	return uid
}

func TestLockedSessionIsRefusedEverywhere(t *testing.T) {
	s := apitest.New(t)
	uid := lockAdmin(t, s, "locked@example.test")
	s.SeedOrg("lockorg", uid)
	c, _ := lockedSession(t, s, uid)

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/orgs", nil},
		{http.MethodGet, "/api/v1/orgs/lockorg/hosts", nil},
		{http.MethodGet, "/api/v1/admin/users", nil},
		{http.MethodPost, "/api/v1/auth/totp/enrol", map[string]any{}},
		{http.MethodPost, "/api/v1/auth/password", map[string]any{"current_password": "x", "new_password": "another long passphrase"}},
		{http.MethodPost, "/api/v1/auth/totp/verify", map[string]any{"code": "123456"}},
		{http.MethodGet, "/api/v1/admin/backups/x/download", nil},
		{http.MethodGet, "/api/explorer", nil},
		{http.MethodGet, "/api/openapi.json", nil},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			resp := requestJSON(t, c, r.method, s.HTTP.URL+r.path, r.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", resp.StatusCode)
			}
			if d := problemDetail(t, resp); d != sessionLocked {
				t.Fatalf("detail %q, want %q", d, sessionLocked)
			}
		})
	}
}

func TestLockedSessionReachesMeAndLogout(t *testing.T) {
	s := apitest.New(t)
	uid := lockAdmin(t, s, "me@example.test")
	c, _ := lockedSession(t, s, uid)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d", resp.StatusCode)
	}
	var me struct {
		Locked bool `json:"locked"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &me); err != nil || !me.Locked {
		t.Fatalf("me.locked = %v (err %v), want true", me.Locked, err)
	}
	resp = requestJSON(t, c, http.MethodPost, s.HTTP.URL+"/api/v1/auth/logout", nil)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("logout: status %d", resp.StatusCode)
	}
}

func TestLockWinsOverPasswordChange(t *testing.T) {
	s := apitest.New(t)
	uid := lockAdmin(t, s, "wins@example.test")
	c, _ := lockedSession(t, s, uid)
	markMustChange(t, s, uid)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != sessionLocked {
		t.Fatalf("detail %q, want %q", d, sessionLocked)
	}
}

func TestBearerTokenIsNeverLocked(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	var owner uuid.UUID
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT user_id FROM org_memberships WHERE org_id = $1 LIMIT 1`, org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	lockedSession(t, s, owner)
	if resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/hosts"); resp.Code != http.StatusOK {
		t.Fatalf("token request: status %d, want 200", resp.Code)
	}
}

func TestMeReportsExpiryAndPINState(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("expiry@example.test", "correct horse battery")
	s.SeedPasskey(uid, "key")
	sid, cookie, err := s.Sessions.Create(t.Context(), uid, true)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: cookie}})
	var want time.Time
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT expires_at FROM sessions WHERE id = $1`, sid).Scan(&want); err != nil {
		t.Fatal(err)
	}

	type meBody struct {
		ExpiresAt string `json:"session_expires_at"`
		PINSet    bool   `json:"lock_pin_set"`
		Prompt    bool   `json:"lock_pin_prompt"`
	}
	read := func() meBody {
		resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
		var m meBody
		if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	m := read()
	got, err := time.Parse(time.RFC3339Nano, m.ExpiresAt)
	if err != nil {
		t.Fatalf("session_expires_at %q: %v", m.ExpiresAt, err)
	}
	if d := got.Sub(want); d > time.Second || d < -time.Second {
		t.Fatalf("session_expires_at %v, want %v", got, want)
	}
	if m.PINSet || !m.Prompt {
		t.Fatalf("fresh account: set=%v prompt=%v, want false/true", m.PINSet, m.Prompt)
	}
	if err := s.Deps.ScreenLock.SetPIN(t.Context(), uid, "4321"); err != nil {
		t.Fatal(err)
	}
	if m = read(); !m.PINSet || m.Prompt {
		t.Fatalf("with PIN: set=%v prompt=%v, want true/false", m.PINSet, m.Prompt)
	}
}
