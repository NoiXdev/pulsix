package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// PULSIX_MFA_OPTIONAL (demo and test instances): an account without a second
// factor signs in with its password alone, and its step-up is a fresh
// password. An account with a factor still proves it, and nothing changes
// with the mode off.

type loginBody struct {
	MFARequired          bool `json:"mfa_required"`
	MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
}

type meFlags struct {
	MFAComplete          bool `json:"mfa_complete"`
	MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
	PasswordStepUp       bool `json:"password_step_up"`
}

func login(t *testing.T, s *apitest.Server, c *http.Client, email string) loginBody {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": email, "password": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out loginBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func meOf(t *testing.T, s *apitest.Server, c *http.Client) meFlags {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out meFlags
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// signInsWithoutFactor counts the audit events that record a sign-in that
// completed without a second factor, and returns the last one's summary.
func signInsWithoutFactor(t *testing.T, s *apitest.Server, userID uuid.UUID) (int, string) {
	t.Helper()
	rows, err := s.DB.Pool().Query(context.Background(),
		`SELECT summary, actor_kind, object_type FROM audit_events WHERE action = 'signInWithoutSecondFactor' AND actor_id = $1 ORDER BY id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n, last := 0, ""
	for rows.Next() {
		var summary, kind, objType string
		if err := rows.Scan(&summary, &kind, &objType); err != nil {
			t.Fatal(err)
		}
		if kind != "user" || objType != "session" {
			t.Fatalf("event actor kind %q, object type %q, want user and session", kind, objType)
		}
		n, last = n+1, summary
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return n, last
}

func TestMFAOptionalLoginCompletesAnAccountWithoutAFactor(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	uid := s.SeedUser("demo@example.test", "correct horse battery")
	s.SeedOrg("demo-org", uid)
	c := s.Client()

	if got := login(t, s, c, "demo@example.test"); got.MFARequired || got.MFAEnrolmentRequired {
		t.Fatalf("login = %+v, want neither a factor to verify nor an enrolment", got)
	}
	if got := meOf(t, s, c); !got.MFAComplete || got.MFAEnrolmentRequired || !got.PasswordStepUp {
		t.Fatalf("me = %+v, want a complete session whose step-up is the password", got)
	}
	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /orgs: %d, want the complete session to reach it", resp.StatusCode)
	}
	n, summary := signInsWithoutFactor(t, s, uid)
	if n != 1 || summary != "signed in without a second factor (optional mode)" {
		t.Fatalf("audit = %d events, last %q", n, summary)
	}
}

func TestMFAOffKeepsTheEnrolmentStep(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("strict@example.test", "correct horse battery")
	c := s.Client()

	if got := login(t, s, c, "strict@example.test"); !got.MFAEnrolmentRequired {
		t.Fatalf("login = %+v, want the enrolment step", got)
	}
	if got := meOf(t, s, c); got.MFAComplete || !got.MFAEnrolmentRequired || got.PasswordStepUp {
		t.Fatalf("me = %+v, want a pending session that must enrol", got)
	}
	if n, _ := signInsWithoutFactor(t, s, uid); n != 0 {
		t.Fatalf("audit = %d sign-ins without a factor, want none", n)
	}
}

func TestMFAOptionalStillVerifiesAnExistingFactor(t *testing.T) {
	for _, kind := range []string{"totp", "passkey"} {
		t.Run(kind, func(t *testing.T) {
			s := apitest.New(t, apitest.WithMFAOptional())
			email := kind + "@example.test"
			uid := s.SeedUser(email, "correct horse battery")
			if kind == "totp" {
				enrolTOTP(t, s.LoginAs(uid), s)
			} else {
				s.SeedPasskey(uid, "yubikey")
			}
			c := s.Client()
			if got := login(t, s, c, email); !got.MFARequired || got.MFAEnrolmentRequired {
				t.Fatalf("login = %+v, want the factor to be verified", got)
			}
			if got := meOf(t, s, c); got.MFAComplete || got.PasswordStepUp {
				t.Fatalf("me = %+v, want a pending session", got)
			}
			if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("GET /orgs: %d, want 403 until the factor is verified", resp.StatusCode)
			}
			if n, _ := signInsWithoutFactor(t, s, uid); n != 0 {
				t.Fatalf("audit = %d sign-ins without a factor, want none", n)
			}
		})
	}
}

func TestSetupStatusReportsMFAOptional(t *testing.T) {
	for _, optional := range []bool{false, true} {
		var opts []apitest.Option
		if optional {
			opts = append(opts, apitest.WithMFAOptional())
		}
		s := apitest.New(t, opts...)
		resp := requestJSON(t, s.Client(), http.MethodGet, s.HTTP.URL+"/api/v1/setup/status", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		var out struct {
			MFAOptional *bool `json:"mfa_optional"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.MFAOptional == nil || *out.MFAOptional != optional {
			t.Fatalf("mfa_optional = %v, want %v (public, before any session)", out.MFAOptional, optional)
		}
	}
}

func TestMFAOptionalSetupOpensACompleteSession(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	c := s.Client()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("root@example.test", "acme"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("complete: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		UserID uuid.UUID `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if got := meOf(t, s, c); !got.MFAComplete || got.MFAEnrolmentRequired {
		t.Fatalf("me after setup = %+v, want a complete session", got)
	}
	if n, _ := signInsWithoutFactor(t, s, out.UserID); n != 1 {
		t.Fatalf("audit = %d sign-ins without a factor, want 1", n)
	}
}

func TestMFAOptionalInviteAcceptOpensACompleteSession(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	owner := s.SeedUser("invite-owner@example.test", "correct horse battery")
	s.SeedOrg("invite-org", owner)
	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/invite-org/invites", map[string]any{"email": "invitee@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	sent := s.Mail.Sent()
	if len(sent) != 1 {
		t.Fatalf("mail sent = %+v", sent)
	}
	const marker = "/invite#token="
	i := strings.Index(sent[0].Body, marker)
	if i < 0 {
		t.Fatalf("invite mail has no token link: %q", sent[0].Body)
	}
	token := strings.TrimSpace(sent[0].Body[i+len(marker):])
	if nl := strings.IndexAny(token, "\n\r"); nl >= 0 {
		token = token[:nl]
	}
	c := s.Client()
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Invitee", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		UserID uuid.UUID `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if got := meOf(t, s, c); !got.MFAComplete {
		t.Fatalf("me after accepting = %+v, want a complete session", got)
	}
	if n, _ := signInsWithoutFactor(t, s, out.UserID); n != 1 {
		t.Fatalf("audit = %d sign-ins without a factor, want 1", n)
	}
}

// A factor-less account's step-up in this mode is a fresh password: a stale
// session is refused the account-security actions until it re-enters it.
func TestMFAOptionalStepUpByPassword(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	uid := s.SeedUser("stepup-pw@example.test", "correct horse battery")
	s.SeedOrg("stepup-pw-org", uid)
	c := s.Client()
	login(t, s, c, "stepup-pw@example.test")

	// The sign-in itself was the proof: the window is open.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh sign-in: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}

	stale(t, s, uid)
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(mustReadBody(t, resp), "step_up_required") {
		t.Fatalf("stale session: %d, want 403 step_up_required", resp.StatusCode)
	}

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password/reauth", map[string]string{"password": "wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d, want 401", resp.StatusCode)
	}
	before := sessionCookieOf(t, s, c)
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password/reauth", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("password step-up: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	if after := sessionCookieOf(t, s, c); after == before {
		t.Fatal("the step-up must rotate the session cookie, as every factor proof does")
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("after the password step-up: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
}

// The password is never a step-up for an account that has a factor, and
// never one with the mode off.
func TestPasswordStepUpRefusedOutsideTheOptionalMode(t *testing.T) {
	t.Run("account with a factor, mode on", func(t *testing.T) {
		s := apitest.New(t, apitest.WithMFAOptional())
		uid := s.SeedUser("pw-factor@example.test", "correct horse battery")
		s.SeedPasskey(uid, "yubikey")
		c := s.LoginAs(uid)
		stale(t, s, uid)
		if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password/reauth", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("password step-up: %d, want 403", resp.StatusCode)
		}
		if got := meOf(t, s, c); got.PasswordStepUp {
			t.Fatal("me.password_step_up must be false for an account with a factor")
		}
		resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("still stale: %d, want 403", resp.StatusCode)
		}
	})
	t.Run("account without a factor, mode off", func(t *testing.T) {
		s := apitest.New(t)
		uid := s.SeedUser("pw-off@example.test", "correct horse battery")
		c := s.LoginAs(uid)
		if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password/reauth", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("password step-up: %d, want 403", resp.StatusCode)
		}
		if got := meOf(t, s, c); got.PasswordStepUp {
			t.Fatal("me.password_step_up must be false with the mode off")
		}
	})
}

// The session renewal asks a factor-less account for its password alone in
// this mode; an account with a factor still needs the factor.
func TestMFAOptionalRenewalByPassword(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	s.SeedUser("renew-pw@example.test", "correct horse battery")
	c := s.Client()
	login(t, s, c, "renew-pw@example.test")
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/session/renew", map[string]string{"password": "wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d, want 401", resp.StatusCode)
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/session/renew", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("renewal by password: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}

	other := s.SeedUser("renew-factor@example.test", "correct horse battery")
	s.SeedPasskey(other, "yubikey")
	oc := s.LoginAs(other)
	if resp := postJSON(t, oc, s.HTTP.URL+"/api/v1/auth/session/renew", map[string]string{"password": "correct horse battery"}); resp.StatusCode == http.StatusOK {
		t.Fatal("an account with a factor must not renew by password alone")
	}
}
