package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// deleteJSON issues a DELETE with a JSON body; net/http.Client has no
// shorthand for that (unlike Post), so this mirrors postJSON above.
func deleteJSON(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodDelete, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestTOTPGatesTheSession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("mfa@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	var enrol struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enrol)
	code, _ := totp.GenerateCode(enrol.Secret, time.Now())
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d", resp.StatusCode)
	}

	fresh := s.Client()
	resp = postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "mfa@example.test", "password": "correct horse battery"})
	var login struct {
		MFARequired bool `json:"mfa_required"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&login)
	if !login.MFARequired {
		t.Fatal("expected mfa_required")
	}
	me, _ := fresh.Get(s.HTTP.URL + "/api/v1/auth/me")
	var meBody struct {
		MFAComplete bool `json:"mfa_complete"`
	}
	_ = json.NewDecoder(me.Body).Decode(&meBody)
	if meBody.MFAComplete {
		t.Fatal("session complete before second factor")
	}
	code, _ = totp.GenerateCode(enrol.Secret, time.Now().Add(30*time.Second))
	resp = postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: %d", resp.StatusCode)
	}
	me, _ = fresh.Get(s.HTTP.URL + "/api/v1/auth/me")
	_ = json.NewDecoder(me.Body).Decode(&meBody)
	if !meBody.MFAComplete {
		t.Fatal("session not upgraded")
	}
}

// TestTOTPConfirmRejectsWrongCode asserts that confirming with a code that
// does not validate against the freshly enrolled secret is rejected (400),
// and that enrolment is not left half-activated by the attempt.
func TestTOTPConfirmRejectsWrongCode(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("bad-code@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": "000000"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirm with wrong code: status %d, want 400", resp.StatusCode)
	}
}

// TestTOTPMutatingRoutesRequireMFAComplete asserts that enrol, confirm and
// disable all reject a session whose second factor is still outstanding
// (mfa_complete = false), per the task's gating rule: only logout, me,
// totp/verify and passkey login may be reached from that state. Without
// this, a session obtained only via password (e.g. a stolen cookie before
// its owner completes TOTP) could enrol a brand-new factor for the account
// or rip out an existing one, neither of which requires ever proving
// possession of a second factor.
func TestTOTPMutatingRoutesRequireMFAComplete(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pending-mfa@example.test", "correct horse battery")
	pending := s.LoginAsPending(uid)

	if resp := postJSON(t, pending, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("enrol from a pending session: status %d, want 403", resp.StatusCode)
	}
	if resp := postJSON(t, pending, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": "000000"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("confirm from a pending session: status %d, want 403", resp.StatusCode)
	}
	if resp := deleteJSON(t, pending, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": "000000"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disable from a pending session: status %d, want 403", resp.StatusCode)
	}
}

// TestTOTPDisableRequiresFreshCodeAndRemovesState asserts that DELETE
// /auth/totp (a) rejects a wrong code instead of disabling on the strength
// of the session cookie alone, and (b) on a correct code removes TOTP
// entirely — a subsequent login for the same account must no longer
// require a second factor.
func TestTOTPDisableRequiresFreshCodeAndRemovesState(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("disable-mfa@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	var enrol struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enrol)
	code, _ := totp.GenerateCode(enrol.Secret, time.Now())
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code}); resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d", resp.StatusCode)
	}

	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": "000000"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disable with a wrong code: status %d, want 401", resp.StatusCode)
	}

	freshCode, _ := totp.GenerateCode(enrol.Secret, time.Now().Add(30*time.Second))
	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": freshCode}); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable with a fresh code: status %d, want 200", resp.StatusCode)
	}

	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "disable-mfa@example.test", "password": "correct horse battery"})
	var login struct {
		MFARequired bool `json:"mfa_required"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&login)
	if login.MFARequired {
		t.Fatal("login still requires MFA after Disable")
	}
}
