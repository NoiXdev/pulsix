package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
)

const renewPassword = "correct horse battery"

// renewFixture is an account with a password and a confirmed TOTP factor,
// signed in with an MFA-complete session.
type renewFixture struct {
	s      *apitest.Server
	uid    uuid.UUID
	c      *http.Client
	secret string
	codes  []string
}

func newRenewFixture(t *testing.T, email string, opts ...apitest.Option) renewFixture {
	t.Helper()
	s := apitest.New(t, opts...)
	uid := s.SeedUser(email, renewPassword)
	c := s.LoginAs(uid)
	secret, codes := enrolTOTP(t, c, s)
	return renewFixture{s: s, uid: uid, c: c, secret: secret, codes: codes}
}

// nextCode is a code for the step after the current one: enrolment
// consumed the current step's.
func (f renewFixture) nextCode(t *testing.T) string {
	t.Helper()
	code, err := totp.GenerateCode(f.secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (f renewFixture) renew(t *testing.T, c *http.Client, body map[string]string) *http.Response {
	t.Helper()
	return postJSON(t, c, f.s.HTTP.URL+"/api/v1/auth/session/renew", body)
}

func (f renewFixture) attempts(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.s.DB.Pool().QueryRow(t.Context(), `SELECT failed_factor_attempts FROM totp_secrets WHERE user_id = $1`, f.uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sessionCookieOf is the session cookie c's jar holds for the server.
func sessionCookieOf(t *testing.T, s *apitest.Server, c *http.Client) string {
	t.Helper()
	u, _ := url.Parse(s.HTTP.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == auth.CookieName {
			return ck.Value
		}
	}
	t.Fatal("no session cookie in the jar")
	return ""
}

// sessionIDOfCookie is the session id a cookie value "<id>.<secret>" names.
func sessionIDOfCookie(t *testing.T, value string) uuid.UUID {
	t.Helper()
	idStr, _, _ := strings.Cut(value, ".")
	id, err := uuid.Parse(idStr)
	if err != nil {
		t.Fatalf("cookie %q names no session: %v", value, err)
	}
	return id
}

// clientWithCookie is a fresh client holding only the given session cookie.
func clientWithCookie(s *apitest.Server, value string) *http.Client {
	c := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: value}})
	return c
}

func liveSessionCount(t *testing.T, s *apitest.Server, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type renewBody struct {
	SessionExpiresAt time.Time `json:"session_expires_at"`
}

// assertRenewed checks a successful renewal answer: a new session cookie
// that works, the old one that no longer does, and an expiry a fresh
// session lifetime away. It returns the new session's id.
func assertRenewed(t *testing.T, s *apitest.Server, c *http.Client, resp *http.Response, oldCookie string, start time.Time) uuid.UUID {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("renew: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var body renewBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if d := body.SessionExpiresAt.Sub(start.Add(auth.SessionTTL)); d < -time.Minute || d > time.Minute {
		t.Fatalf("session_expires_at = %v, want about %v", body.SessionExpiresAt, start.Add(auth.SessionTTL))
	}
	var set *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName {
			set = ck
		}
	}
	if set == nil || set.Value == "" || set.Value == oldCookie || !set.HttpOnly || set.Path != "/" || set.MaxAge != int(auth.SessionTTL.Seconds()) {
		t.Fatalf("Set-Cookie = %+v, want a new session cookie", set)
	}
	if sessionIDOfCookie(t, set.Value) == sessionIDOfCookie(t, oldCookie) {
		t.Fatal("the renewal kept the session id; it must replace the session")
	}
	me := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me with the new cookie: %d", me.StatusCode)
	}
	var meBody renewBody
	if err := json.NewDecoder(me.Body).Decode(&meBody); err != nil {
		t.Fatal(err)
	}
	if !meBody.SessionExpiresAt.Equal(body.SessionExpiresAt) {
		t.Fatalf("/auth/me expiry %v, renew answered %v", meBody.SessionExpiresAt, body.SessionExpiresAt)
	}
	if old := requestJSON(t, clientWithCookie(s, oldCookie), http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); old.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me with the old cookie: %d, want 401", old.StatusCode)
	}
	return sessionIDOfCookie(t, set.Value)
}

func TestRenewReplacesTheSession(t *testing.T) {
	f := newRenewFixture(t, "renew@example.test")
	oldCookie := sessionCookieOf(t, f.s, f.c)
	start := time.Now()
	resp := f.renew(t, f.c, map[string]string{"password": renewPassword, "code": f.nextCode(t)})
	newID := assertRenewed(t, f.s, f.c, resp, oldCookie, start)
	if n := liveSessionCount(t, f.s, f.uid); n != 1 {
		t.Fatalf("live sessions = %d, want 1", n)
	}

	ev := findEvent(t, myActivity(t, f.s, f.c), "POST renewSession")
	if ev.OrgID != nil || ev.ActorID == nil || *ev.ActorID != f.uid.String() {
		t.Fatalf("event scope = org %v actor %v, want no org and %s", ev.OrgID, ev.ActorID, f.uid)
	}
	oldID := sessionIDOfCookie(t, oldCookie)
	if ev.ObjectType != "session" || ev.ObjectID != newID.String() || !strings.Contains(ev.Summary, oldID.String()) {
		t.Fatalf("event = %+v, want session %s naming the replaced %s", ev, newID, oldID)
	}
	if strings.Contains(ev.Summary, renewPassword) {
		t.Fatal("the audit summary carries the password")
	}
}

// The password is checked before the second factor: a wrong password
// spends no factor attempt and consumes no code, so the same code still
// renews right afterwards.
func TestRenewWrongPasswordSpendsNoFactorAttempt(t *testing.T) {
	f := newRenewFixture(t, "renew-wrong@example.test")
	code := f.nextCode(t)
	before := f.attempts(t)
	resp := f.renew(t, f.c, map[string]string{"password": "wrong horse battery", "code": code})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d, want 401", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != "invalid credentials" {
		t.Fatalf("wrong password: detail %q", d)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName {
			t.Fatalf("a refused renewal set a cookie: %+v", ck)
		}
	}
	if after := f.attempts(t); after != before {
		t.Fatalf("factor attempts %d -> %d after a wrong password, want unchanged", before, after)
	}
	oldCookie := sessionCookieOf(t, f.s, f.c)
	resp = f.renew(t, f.c, map[string]string{"password": renewPassword, "code": code})
	assertRenewed(t, f.s, f.c, resp, oldCookie, time.Now())
}

func TestRenewWrongCodeSpendsAFactorAttempt(t *testing.T) {
	f := newRenewFixture(t, "renew-wrong-code@example.test")
	before := f.attempts(t)
	resp := f.renew(t, f.c, map[string]string{"password": renewPassword, "code": wrongTOTPCode(t, f.secret)})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code: status %d, want 401", resp.StatusCode)
	}
	if after := f.attempts(t); after != before+1 {
		t.Fatalf("factor attempts %d -> %d, want one spent", before, after)
	}
	if n := liveSessionCount(t, f.s, f.uid); n != 1 {
		t.Fatalf("live sessions = %d, want the old one only", n)
	}
	if me := requestJSON(t, f.c, http.MethodGet, f.s.HTTP.URL+"/api/v1/auth/me", nil); me.StatusCode != http.StatusOK {
		t.Fatalf("the old session after a refused renewal: %d, want 200", me.StatusCode)
	}
}

func TestRenewWithRecoveryCode(t *testing.T) {
	f := newRenewFixture(t, "renew-recovery@example.test")
	oldCookie := sessionCookieOf(t, f.s, f.c)
	resp := f.renew(t, f.c, map[string]string{"password": renewPassword, "recovery_code": f.codes[0]})
	assertRenewed(t, f.s, f.c, resp, oldCookie, time.Now())

	// Consumed: the same recovery code does not renew twice.
	resp = f.renew(t, f.c, map[string]string{"password": renewPassword, "recovery_code": f.codes[0]})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a used recovery code: status %d, want 401", resp.StatusCode)
	}
}

// A TOTP code is consumed as at sign-in: the code a sign-in just used does
// not renew the session it signed in.
func TestRenewReplayedTOTPCodeFails(t *testing.T) {
	f := newRenewFixture(t, "renew-replay@example.test")
	fresh := f.s.Client()
	if resp := postJSON(t, fresh, f.s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "renew-replay@example.test", "password": renewPassword}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	code := f.nextCode(t)
	if resp := postJSON(t, fresh, f.s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"code": code}); resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: %d", resp.StatusCode)
	}
	resp := f.renew(t, fresh, map[string]string{"password": renewPassword, "code": code})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed code: status %d, want 401", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != "invalid code" {
		t.Fatalf("replayed code: detail %q", d)
	}
}

// A locked session cannot be renewed (spec anti-goal: someone at a
// stranger's desk must not keep the stranger's session alive). It simply
// runs out.
func TestRenewRefusedWhileLocked(t *testing.T) {
	f := newRenewFixture(t, "renew-locked@example.test")
	if resp := requestJSON(t, f.c, http.MethodPut, f.s.HTTP.URL+"/api/v1/auth/lock/pin", map[string]string{"pin": "1234"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set PIN: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, f.c, f.s.HTTP.URL+"/api/v1/auth/lock", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("lock: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	before := f.attempts(t)
	for _, r := range []struct {
		path string
		body any
	}{
		{"/api/v1/auth/session/renew", map[string]string{"password": renewPassword, "code": f.nextCode(t)}},
		{"/api/v1/auth/session/renew/passkey", map[string]any{"ceremony_id": uuid.Must(uuid.NewV7()).String(), "response": map[string]any{}}},
		{"/api/v1/auth/passkeys/reauth/begin", struct{}{}},
		{"/api/v1/auth/passkeys/reauth/finish", map[string]any{"ceremony_id": uuid.Must(uuid.NewV7()).String(), "response": map[string]any{}}},
	} {
		resp := postJSON(t, f.c, f.s.HTTP.URL+r.path, r.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s while locked: status %d, want 403", r.path, resp.StatusCode)
		}
		if d := problemDetail(t, resp); d != sessionLocked {
			t.Fatalf("%s while locked: detail %q, want %q", r.path, d, sessionLocked)
		}
	}
	if after := f.attempts(t); after != before {
		t.Fatalf("factor attempts %d -> %d while locked, want unchanged", before, after)
	}
	if n := liveSessionCount(t, f.s, f.uid); n != 1 {
		t.Fatalf("live sessions = %d, want the locked one only", n)
	}
}

// A bearer token has no session to renew; the root auth routes do not
// resolve it at all.
func TestRenewRefusesBearerToken(t *testing.T) {
	s := apitest.New(t)
	_, tok := s.OrgWithToken(t)
	assertion := map[string]any{"ceremony_id": uuid.Must(uuid.NewV7()).String(), "response": map[string]any{}}
	for _, r := range []struct {
		path string
		body any
	}{
		{"/api/v1/auth/session/renew", map[string]string{"password": renewPassword, "code": "123456"}},
		{"/api/v1/auth/session/renew/passkey", assertion},
		{"/api/v1/auth/passkeys/reauth/begin", struct{}{}},
		{"/api/v1/auth/passkeys/reauth/finish", assertion},
	} {
		if resp := s.Post(t, tok, r.path, r.body); resp.Code != http.StatusUnauthorized {
			t.Fatalf("%s with a bearer token: status %d, want 401", r.path, resp.Code)
		}
	}
}

// A pending session has proved no second factor yet; it signs in through
// /auth/totp/verify or the passkey sign-in, not through a renewal.
func TestRenewRefusesPendingSession(t *testing.T) {
	f := newRenewFixture(t, "renew-pending@example.test")
	pending := f.s.LoginAsPending(f.uid)
	resp := f.renew(t, pending, map[string]string{"password": renewPassword, "code": f.nextCode(t)})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pending session: status %d, want 403", resp.StatusCode)
	}
}

// Guessing the password through a renewal is rate limited like a login.
func TestRenewIsRateLimited(t *testing.T) {
	f := newRenewFixture(t, "renew-limit@example.test", apitest.WithAuthRateLimit(2))
	for i := range 2 {
		if resp := f.renew(t, f.c, map[string]string{"password": "wrong horse battery", "code": "000000"}); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i+1, resp.StatusCode)
		}
	}
	if resp := f.renew(t, f.c, map[string]string{"password": renewPassword, "code": f.nextCode(t)}); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third attempt: status %d, want 429", resp.StatusCode)
	}
}
