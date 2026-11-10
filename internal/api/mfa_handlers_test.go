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

// TestMeReportsTOTPConfirmed covers fix round 1's Important 4: /auth/me's
// mfa_complete alone cannot tell the SPA whether a real second factor
// exists (a session with none enrolled at all is still mfa_complete —
// Login only steps up when there is something to step up to), so the SPA
// needs a separate, honest signal. totp_confirmed must be false before
// enrolment and true immediately after ConfirmEnrol, using the same
// session throughout.
func TestMeReportsTOTPConfirmed(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("totp-confirmed@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	me, _ := c.Get(s.HTTP.URL + "/api/v1/auth/me")
	var meBody struct {
		TOTPConfirmed bool `json:"totp_confirmed"`
	}
	_ = json.NewDecoder(me.Body).Decode(&meBody)
	if meBody.TOTPConfirmed {
		t.Fatal("totp_confirmed = true before any enrolment")
	}

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	var enrol struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enrol)

	me, _ = c.Get(s.HTTP.URL + "/api/v1/auth/me")
	_ = json.NewDecoder(me.Body).Decode(&meBody)
	if meBody.TOTPConfirmed {
		t.Fatal("totp_confirmed = true before confirmation (unconfirmed secret must not count)")
	}

	code, _ := totp.GenerateCode(enrol.Secret, time.Now())
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code}); resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d", resp.StatusCode)
	}

	me, _ = c.Get(s.HTTP.URL + "/api/v1/auth/me")
	_ = json.NewDecoder(me.Body).Decode(&meBody)
	if !meBody.TOTPConfirmed {
		t.Fatal("totp_confirmed = false after confirmation")
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
// disable all reject a session whose account has a second factor it has
// not verified yet (mfa_complete = false): only logout, me, totp/verify and
// passkey login may be reached from that state. Without this, a session
// obtained only via password (e.g. a stolen cookie before its owner
// completes TOTP) could enrol a brand-new factor for the account or rip out
// an existing one, neither of which requires ever proving possession of a
// second factor. (An account with no factor at all is the other pending
// state, covered by TestEnrolmentSessionCanOnlyEnrol.)
func TestTOTPMutatingRoutesRequireMFAComplete(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pending-mfa@example.test", "correct horse battery")
	s.SeedPasskey(uid, "existing key")
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
	// A second factor is mandatory: TOTP can be disabled only while the
	// account keeps another one (TestTOTPDisableRefusesTheLastFactor).
	s.SeedPasskey(uid, "fallback key")

	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": "000000"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disable with a wrong code: status %d, want 401", resp.StatusCode)
	}

	freshCode, _ := totp.GenerateCode(enrol.Secret, time.Now().Add(30*time.Second))
	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": freshCode}); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable with a fresh code: status %d, want 200", resp.StatusCode)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	var me struct {
		TOTPConfirmed bool `json:"totp_confirmed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if me.TOTPConfirmed {
		t.Fatal("TOTP still confirmed after Disable")
	}
}

// TestTOTPEnrolRequiresFreshCodeOnReEnrolment is the HTTP-level assertion
// for Finding 2, fix round 1: once TOTP is confirmed, POST
// /auth/totp/enrol must require a fresh valid code exactly like DELETE
// /auth/totp does. Before this fix, calling enrol a second time reset
// confirmed_at with no code at all, so an attacker holding an
// MFA-complete session could strip the factor either through this
// endpoint or through disable.
func TestTOTPEnrolRequiresFreshCodeOnReEnrolment(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("re-enrol@example.test", "correct horse battery")
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

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("re-enrol without a code: status %d, want 400", resp.StatusCode)
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", map[string]string{"code": "000000"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("re-enrol with a wrong code: status %d, want 400", resp.StatusCode)
	}

	freshCode, _ := totp.GenerateCode(enrol.Secret, time.Now().Add(30*time.Second))
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", map[string]string{"code": freshCode})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-enrol with a fresh code: status %d, want 200", resp.StatusCode)
	}
	var reEnrol struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&reEnrol)
	if reEnrol.Secret == "" || reEnrol.Secret == enrol.Secret {
		t.Fatalf("re-enrol did not issue a new secret: got %q, original %q", reEnrol.Secret, enrol.Secret)
	}
}

// TestTOTPVerifyLocksTheAccountAfterRepeatedFailures is backlog 7.26 over
// HTTP: the per-address limit is not the only one any more. After five
// wrong codes the account's second factor is locked, so even the right
// code is refused with 429 — whichever address it comes from.
func TestTOTPVerifyLocksTheAccountAfterRepeatedFailures(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("guess@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	var enrol struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enrol)
	code, _ := totp.GenerateCode(enrol.Secret, time.Now())
	if resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code}); resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d", resp.StatusCode)
	}

	fresh := s.Client()
	if resp = postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "guess@example.test", "password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for i := range 5 {
		resp = postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"code": "000000"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: %d, want 401", i+1, resp.StatusCode)
		}
	}
	code, _ = totp.GenerateCode(enrol.Secret, time.Now().Add(30*time.Second))
	if resp = postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"code": code}); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the right code on a locked account: %d, want 429", resp.StatusCode)
	}
}

// TestTOTPVerifyWithAWrongCodeStillSpendsAnAttempt guards backlog 7.118's
// ruling against 7.26: verify completes the session in a transaction that
// takes the account row first, and the code is checked before and outside
// it. A wrong code must still spend one of the account's factor attempts;
// checked inside the transaction, the rollback that follows the refusal
// would refund it and lift the brute-force budget.
func TestTOTPVerifyWithAWrongCodeStillSpendsAnAttempt(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("spent@example.test", "correct horse battery")
	secret, _ := enrolTOTP(t, s.LoginAs(uid), s)

	fresh := s.Client()
	if resp := postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "spent@example.test", "password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	attempts := func() int {
		t.Helper()
		var n int
		if err := s.DB.Pool().QueryRow(t.Context(),
			`SELECT failed_factor_attempts FROM totp_secrets WHERE user_id = $1`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := attempts()

	resp := postJSON(t, fresh, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"code": wrongTOTPCode(t, secret)})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("verify with a wrong code: %d, want 401", resp.StatusCode)
	}
	if after := attempts(); after != before+1 {
		t.Fatalf("factor attempts after a wrong code = %d, want %d", after, before+1)
	}
}

// wrongTOTPCode is a six-digit code that is valid for secret in no time
// step near now, so a test cannot pass it by chance.
func wrongTOTPCode(t *testing.T, secret string) string {
	t.Helper()
	valid := map[string]bool{}
	for step := -2; step <= 2; step++ {
		code, err := totp.GenerateCode(secret, time.Now().Add(time.Duration(step)*30*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		valid[code] = true
	}
	for _, c := range []string{"000000", "111111", "222222", "333333", "444444", "555555"} {
		if !valid[c] {
			return c
		}
	}
	t.Fatal("no wrong code left")
	return ""
}
