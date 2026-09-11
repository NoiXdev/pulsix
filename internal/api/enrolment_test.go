package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// meState is the part of GET /auth/me the second-factor tests read.
type meState struct {
	MFAComplete          bool `json:"mfa_complete"`
	MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
	TOTPConfirmed        bool `json:"totp_confirmed"`
}

func readMe(t *testing.T, s *apitest.Server, c *http.Client) meState {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/me: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var me meState
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	return me
}

// passwordLogin signs email in with a password on a fresh client and
// returns the client together with the login response's flags.
func passwordLogin(t *testing.T, s *apitest.Server, email, password string) (*http.Client, bool, bool) {
	t.Helper()
	c := s.Client()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": email, "password": password})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var body struct {
		MFARequired          bool `json:"mfa_required"`
		MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return c, body.MFARequired, body.MFAEnrolmentRequired
}

// TestEnrolmentSessionCanOnlyEnrol covers the session a password login
// opens for an account without a second factor: it is not MFA-complete, it
// reaches /auth/me, logout and the enrolment routes (requireEnrolmentSession)
// and nothing else — no organization route, no org list, no activity feed,
// no factor removal — and confirming a TOTP secret completes that very
// session with no fresh login.
func TestEnrolmentSessionCanOnlyEnrol(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("enrol-only@example.test", "correct horse battery")
	s.SeedOrg("enrol-org", uid)

	c, mfaRequired, enrolmentRequired := passwordLogin(t, s, "enrol-only@example.test", "correct horse battery")
	if mfaRequired || !enrolmentRequired {
		t.Fatalf("login flags: mfa_required=%v mfa_enrolment_required=%v, want false/true", mfaRequired, enrolmentRequired)
	}
	if me := readMe(t, s, c); me.MFAComplete || !me.MFAEnrolmentRequired {
		t.Fatalf("me before enrolment = %+v, want mfa_complete=false, mfa_enrolment_required=true", me)
	}

	refused := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/orgs/enrol-org/hosts", nil},
		{http.MethodGet, "/api/v1/orgs/enrol-org", nil},
		{http.MethodGet, "/api/v1/orgs", nil},
		{http.MethodPost, "/api/v1/orgs", map[string]string{"slug": "sneaky", "name": "Sneaky"}},
		{http.MethodGet, "/api/v1/me/activity", nil},
		{http.MethodDelete, "/api/v1/auth/totp", map[string]string{"code": "000000"}},
		{http.MethodDelete, "/api/v1/auth/passkeys/00000000-0000-7000-8000-000000000000", nil},
	}
	for _, r := range refused {
		if resp := requestJSON(t, c, r.method, s.HTTP.URL+r.path, r.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s before enrolment: status %d, want 403", r.method, r.path, resp.StatusCode)
		}
	}

	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/passkeys", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/passkeys before enrolment: status %d, want 200", resp.StatusCode)
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("passkey register/begin before enrolment: status %d, want 200", resp.StatusCode)
	}

	enrolAndConfirmTOTP(t, s, c)

	if me := readMe(t, s, c); !me.MFAComplete || me.MFAEnrolmentRequired || !me.TOTPConfirmed {
		t.Fatalf("me after enrolment = %+v, want an MFA-complete session with TOTP confirmed", me)
	}
	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/enrol-org/hosts", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("organization route after enrolment, same session: status %d, want 200", resp.StatusCode)
	}

	// The next password login has a factor to verify instead.
	if _, mfaRequired, enrolmentRequired := passwordLogin(t, s, "enrol-only@example.test", "correct horse battery"); !mfaRequired || enrolmentRequired {
		t.Fatalf("login after enrolment: mfa_required=%v mfa_enrolment_required=%v, want true/false", mfaRequired, enrolmentRequired)
	}
}

// TestOtherPendingSessionsLoseEnrolmentAccess covers why
// requireEnrolmentSession reads the account's factors live instead of
// remembering them from login: once one session enrols a factor, a second
// session that was opened before — say, by someone else holding the
// password — can no longer add one of its own and must verify instead.
func TestOtherPendingSessionsLoseEnrolmentAccess(t *testing.T) {
	s := apitest.New(t)
	s.SeedUser("two-sessions@example.test", "correct horse battery")
	first, _, _ := passwordLogin(t, s, "two-sessions@example.test", "correct horse battery")
	second, _, _ := passwordLogin(t, s, "two-sessions@example.test", "correct horse battery")

	enrolAndConfirmTOTP(t, s, first)

	if me := readMe(t, s, second); me.MFAComplete || me.MFAEnrolmentRequired {
		t.Fatalf("second session after the account enrolled = %+v, want pending verification", me)
	}
	if resp := postJSON(t, second, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("TOTP enrol from the second session: status %d, want 403", resp.StatusCode)
	}
	if resp := postJSON(t, second, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("passkey register/begin from the second session: status %d, want 403", resp.StatusCode)
	}
}

// TestPasskeyRegistrationCompletesEnrolmentSession is the passkey half of
// "once a factor is confirmed, the session becomes MFA-complete without a
// fresh login".
func TestPasskeyRegistrationCompletesEnrolmentSession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("enrol-passkey@example.test", "correct horse battery")
	s.SeedOrg("enrol-passkey-org", uid)
	c, _, _ := passwordLogin(t, s, "enrol-passkey@example.test", "correct horse battery")

	registerPasskeyForTest(t, s, c, "First Key", []byte("enrol-passkey-cred"))

	if me := readMe(t, s, c); !me.MFAComplete || me.MFAEnrolmentRequired {
		t.Fatalf("me after passkey registration = %+v, want MFA-complete", me)
	}
	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/enrol-passkey-org/hosts", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("organization route after passkey enrolment: status %d, want 200", resp.StatusCode)
	}
}

// TestTOTPDisableRefusesTheLastFactor: a second factor is mandatory, so
// disabling TOTP on an account that has nothing else is refused with 409
// and leaves TOTP in place.
func TestTOTPDisableRefusesTheLastFactor(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("last-totp@example.test", "correct horse battery")
	c := s.LoginAs(uid)
	secret := enrolAndConfirmTOTP(t, s, c)

	fresh, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": fresh}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("disabling the last factor: status %d, want 409", resp.StatusCode)
	}
	if me := readMe(t, s, c); !me.TOTPConfirmed {
		t.Fatal("TOTP was removed although the refusal said otherwise")
	}
}

// TestPasskeyDeleteRefusesTheLastFactor is the passkey counterpart: the only
// passkey of an account without TOTP cannot be removed, while one of two
// can.
func TestPasskeyDeleteRefusesTheLastFactor(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("last-passkey@example.test", "correct horse battery")
	c := s.LoginAs(uid)
	only := s.SeedPasskey(uid, "only key")

	if resp := requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/"+only.String(), nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleting the last factor: status %d, want 409", resp.StatusCode)
	}
	s.SeedPasskey(uid, "second key")
	if resp := requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/"+only.String(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("deleting one of two passkeys: status %d, want 200", resp.StatusCode)
	}
}
