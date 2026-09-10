package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

func completeSetupBody(email, orgSlug string) map[string]any {
	return map[string]any{
		"admin_email": email, "admin_password": "correct horse battery", "admin_name": "Root Admin",
		"org_slug": orgSlug, "org_name": "Acme",
	}
}

// TestSetupStatusCompleteFlow walks the whole first-run wizard against a
// fresh database, mirroring the brief's step 4 test sketch: status starts
// incomplete, completing it opens a session the SPA can use immediately
// (GET /auth/me reports instance_admin), status flips to completed, and a
// second attempt is refused.
func TestSetupStatusCompleteFlow(t *testing.T) {
	s := apitest.New(t)
	c := s.Client()

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/setup/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status before setup: %d", resp.StatusCode)
	}
	var before struct {
		Completed        bool   `json:"completed"`
		AdminMFAEnrolled bool   `json:"admin_mfa_enrolled"`
		Version          string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&before); err != nil {
		t.Fatal(err)
	}
	if before.Completed || before.AdminMFAEnrolled || before.Version == "" {
		t.Fatalf("status before setup = %+v", before)
	}

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("root@example.test", "acme"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("complete: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var completeOut struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"org_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&completeOut); err != nil {
		t.Fatal(err)
	}
	if completeOut.UserID == "" || completeOut.OrgID == "" {
		t.Fatalf("complete body = %+v", completeOut)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("complete did not set a session cookie")
	}

	me := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me after setup: status %d", me.StatusCode)
	}
	var meBody struct {
		InstanceAdmin bool `json:"instance_admin"`
		MFAComplete   bool `json:"mfa_complete"`
	}
	if err := json.NewDecoder(me.Body).Decode(&meBody); err != nil {
		t.Fatal(err)
	}
	if !meBody.InstanceAdmin {
		t.Fatal("the setup response's session is not the instance admin")
	}
	// Finding 1, fix round 1: the wizard's session must be MFA-complete,
	// matching what auth.Service.Login already gives any account with no
	// second factor enrolled. A pending (MFA-incomplete) session here would
	// 403 at every credential-enrolment route (they all sit behind
	// requireInteractiveSession), stranding the administrator with no way
	// to reach TOTP or passkey enrolment except logging out and back in.
	if !meBody.MFAComplete {
		t.Fatal("the setup response's session must be MFA-complete: see setup.Result.Cookie's own doc comment")
	}

	// The real-world consequence of the bug this guards against: the
	// wizard's session must actually be able to reach the enrolment route
	// it hands off to, not just report MFAComplete = true in isolation.
	enrol := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", map[string]any{})
	if enrol.StatusCode != http.StatusOK {
		t.Fatalf("POST /auth/totp/enrol with the wizard's own session: status %d, want 200; body %s", enrol.StatusCode, mustReadBody(t, enrol))
	}
	var enrolBody struct {
		Secret     string `json:"secret"`
		OTPAuthURL string `json:"otpauth_url"`
	}
	if err := json.NewDecoder(enrol.Body).Decode(&enrolBody); err != nil {
		t.Fatal(err)
	}
	if enrolBody.Secret == "" || enrolBody.OTPAuthURL == "" {
		t.Fatalf("totp enrol body = %+v", enrolBody)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/setup/status", nil)
	var after struct {
		Completed bool `json:"completed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Completed {
		t.Fatal("status after setup must report completed = true")
	}

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("second@example.test", "second"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second complete: status %d, want 409", resp.StatusCode)
	}
}

// TestSetupCompleteValidatesInput checks a case the HTTP schema's own tags
// (minLength, pattern) cannot catch by themselves — a name that is only
// whitespace passes minLength:"1" but must still be rejected by
// setup.Input.validate's TrimSpace check — so this exercises the actual
// mapSetupErr wiring, not just huma's request validation.
func TestSetupCompleteValidatesInput(t *testing.T) {
	s := apitest.New(t)
	c := s.Client()
	body := completeSetupBody("root@example.test", "acme")
	body["admin_name"] = " "
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/setup/complete", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestSetupCompleteConcurrentOnlyOneSucceeds is the HTTP-level counterpart
// to internal/setup's SQL concurrency test: two requests racing to
// complete first-run setup through the real API must never both succeed.
//
// The goroutines below deliberately avoid postJSON/requestJSON/
// mustReadBody: all three call t.Fatal on error, and t.Fatal (via
// t.FailNow) is documented to only be safe from the test's own goroutine —
// calling it from one of these spawned goroutines is undefined behaviour
// and can deadlock wg.Wait() instead of failing cleanly. Every error path
// here uses t.Errorf and returns instead.
func TestSetupCompleteConcurrentOnlyOneSucceeds(t *testing.T) {
	s := apitest.New(t)

	const n = 5
	var wg sync.WaitGroup
	var created, conflicted int32
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := s.Client()
			b, err := json.Marshal(completeSetupBody("racer"+string(rune('a'+i))+"@example.test", "racer"+string(rune('a'+i))))
			if err != nil {
				t.Errorf("racer %d: marshal: %v", i, err)
				return
			}
			resp, err := c.Post(s.HTTP.URL+"/api/v1/setup/complete", "application/json", bytes.NewReader(b))
			if err != nil {
				t.Errorf("racer %d: post: %v", i, err)
				return
			}
			defer resp.Body.Close() //nolint:errcheck
			switch resp.StatusCode {
			case http.StatusCreated:
				atomic.AddInt32(&created, 1)
			case http.StatusConflict:
				atomic.AddInt32(&conflicted, 1)
			default:
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("racer %d: unexpected status %d, body %s", i, resp.StatusCode, body)
			}
		}(i)
	}
	wg.Wait()

	if created != 1 {
		t.Fatalf("created = %d, want exactly 1 (conflicted=%d)", created, conflicted)
	}
	if conflicted != n-1 {
		t.Fatalf("conflicted = %d, want %d", conflicted, n-1)
	}
}

// TestSMTPSettingsRoutesRequireInstanceAdmin covers the brief's step 4
// sketch for settings: an instance admin can read and replace the stored
// SMTP configuration (which never comes back with a password), and a
// plain member of some organization is refused.
func TestSMTPSettingsRoutesRequireInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	adminOut := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("wizard-admin@example.test", "wizard"))
	if adminOut.StatusCode != http.StatusCreated {
		t.Fatalf("wizard complete: status %d", adminOut.StatusCode)
	}
	var wizardOut struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(adminOut.Body).Decode(&wizardOut); err != nil {
		t.Fatal(err)
	}

	member := s.SeedUser("member@example.test", "correct horse battery")
	s.SeedOrg("members-org", member)
	memberClient := s.LoginAs(member)

	body := map[string]any{"host": "smtp.example.test", "port": 587, "username": "u", "password": "p", "from": "pulsix@example.test", "start_tls": false}
	resp := requestJSON(t, memberClient, http.MethodPut, s.HTTP.URL+"/api/v1/admin/settings/smtp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT smtp settings as a non-admin: status %d, want 403", resp.StatusCode)
	}

	adminClient := s.LoginAs(uuid.MustParse(wizardOut.UserID))
	resp = requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+"/api/v1/admin/settings/smtp", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET smtp settings before any save: status %d", resp.StatusCode)
	}
	var before struct {
		Configured bool `json:"configured"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&before); err != nil {
		t.Fatal(err)
	}
	if before.Configured {
		t.Fatal("GET smtp settings before any save must report configured = false")
	}

	resp = requestJSON(t, adminClient, http.MethodPut, s.HTTP.URL+"/api/v1/admin/settings/smtp", body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT smtp settings as the admin: status %d, want 204; body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+"/api/v1/admin/settings/smtp", nil)
	// Capture the raw body once and decode from that string for both
	// checks below. The previous version drained the body through
	// json.NewDecoder first and only then called mustReadBody, which reads
	// an already-exhausted response body — containsPassword always saw an
	// empty string and so could never actually fail, no matter what the
	// handler returned.
	raw := mustReadBody(t, resp)
	var body2 struct {
		Configured bool   `json:"configured"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
	}
	if err := json.Unmarshal([]byte(raw), &body2); err != nil {
		t.Fatal(err)
	}
	if !body2.Configured || body2.Host != "smtp.example.test" || body2.Port != 587 {
		t.Fatalf("GET smtp settings after save = %+v", body2)
	}
	if containsPassword(raw) {
		t.Fatalf("smtp settings response leaked the password: %s", raw)
	}
}

// TestSMTPSettingsTestReportsFailureWhenUnconfigured drives the "server's
// actual response" requirement from the least fragile angle available in
// this package (no in-process SMTP server here — that lives in
// internal/mail's own tests): with nothing ever saved, this fails at
// loading settings — deps.Mailer.Test's call into setup.LoadSMTP returns
// setup.ErrNotFound — never even reaching deliver()'s dial step, and that
// must surface as a clear failure, not a silent 200. It is a state
// conflict on this instance (there is nothing to test yet), not a failed
// attempt to reach an upstream server, so the route answers 409 rather
// than the 502 a real dial/auth/delivery failure gets.
func TestSMTPSettingsTestReportsFailureWhenUnconfigured(t *testing.T) {
	s := apitest.New(t)
	adminOut := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("wizard-admin2@example.test", "wizard2"))
	if adminOut.StatusCode != http.StatusCreated {
		t.Fatalf("wizard complete: status %d", adminOut.StatusCode)
	}
	var wizardOut struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(adminOut.Body).Decode(&wizardOut); err != nil {
		t.Fatal(err)
	}
	adminClient := s.LoginAs(uuid.MustParse(wizardOut.UserID))

	resp := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/admin/settings/smtp/test", map[string]any{"to": "dest@example.test"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("smtp test with nothing configured: status %d, want 409; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestSetupStatusAdminMFAEnrolledGatedBehindSession covers the "also fix"
// item from Task 12 fix round 1: GET /setup/status is public (an
// unauthenticated caller must still learn Completed, for the SPA's
// pre-login redirect) but AdminMFAEnrolled reveals whether the instance
// administrator has a second factor — sensitive enough that only a caller
// who at least has a session should see the real value. This drives the
// underlying value to genuinely true (the admin actually enrols and
// confirms TOTP) specifically so a caller that hides it and a caller that
// doesn't are distinguishable — a fresh instance already reports false
// either way, which wouldn't prove the gate does anything.
func TestSetupStatusAdminMFAEnrolledGatedBehindSession(t *testing.T) {
	s := apitest.New(t)
	adminOut := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("mfa-gate-admin@example.test", "mfagate"))
	if adminOut.StatusCode != http.StatusCreated {
		t.Fatalf("wizard complete: status %d", adminOut.StatusCode)
	}
	var wizardOut struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(adminOut.Body).Decode(&wizardOut); err != nil {
		t.Fatal(err)
	}
	adminClient := s.LoginAs(uuid.MustParse(wizardOut.UserID))

	enrol := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/auth/totp/enrol", map[string]any{})
	var enrolBody struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(enrol.Body).Decode(&enrolBody); err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(enrolBody.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirm := postJSON(t, adminClient, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code})
	if confirm.StatusCode != http.StatusOK {
		t.Fatalf("confirm totp: status %d, body %s", confirm.StatusCode, mustReadBody(t, confirm))
	}

	anon := requestJSON(t, s.Client(), http.MethodGet, s.HTTP.URL+"/api/v1/setup/status", nil)
	var anonBody struct {
		Completed        bool `json:"completed"`
		AdminMFAEnrolled bool `json:"admin_mfa_enrolled"`
	}
	if err := json.NewDecoder(anon.Body).Decode(&anonBody); err != nil {
		t.Fatal(err)
	}
	if !anonBody.Completed {
		t.Fatal("status must still report completed = true with no session")
	}
	if anonBody.AdminMFAEnrolled {
		t.Fatal("an unauthenticated caller must not see admin_mfa_enrolled, even though it is genuinely true")
	}

	authed := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+"/api/v1/setup/status", nil)
	var authedBody struct {
		AdminMFAEnrolled bool `json:"admin_mfa_enrolled"`
	}
	if err := json.NewDecoder(authed.Body).Decode(&authedBody); err != nil {
		t.Fatal(err)
	}
	if !authedBody.AdminMFAEnrolled {
		t.Fatal("a caller with a session must see the real admin_mfa_enrolled value")
	}
}

func containsPassword(body string) bool {
	// The sealed password is base64 of AES-GCM ciphertext, never the
	// plaintext "p" the round-trip test writes elsewhere — a raw
	// "password" key in the response at all is already the bug this
	// guards against.
	return jsonHasKey(body, "password")
}

func jsonHasKey(body, key string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
