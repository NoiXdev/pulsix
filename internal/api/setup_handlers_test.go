package api_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

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
	if meBody.MFAComplete {
		t.Fatal("the setup response's session must not be MFA-complete: the admin has no second factor enrolled yet")
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
			resp := postJSON(t, c, s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody(
				"racer"+string(rune('a'+i))+"@example.test", "racer"+string(rune('a'+i))))
			switch resp.StatusCode {
			case http.StatusCreated:
				atomic.AddInt32(&created, 1)
			case http.StatusConflict:
				atomic.AddInt32(&conflicted, 1)
			default:
				t.Errorf("racer %d: unexpected status %d, body %s", i, resp.StatusCode, mustReadBody(t, resp))
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
	body2, err := decodeSMTPGet(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !body2.Configured || body2.Host != "smtp.example.test" || body2.Port != 587 {
		t.Fatalf("GET smtp settings after save = %+v", body2)
	}
	raw := mustReadBody(t, resp)
	if containsPassword(raw) {
		t.Fatalf("smtp settings response leaked the password: %s", raw)
	}
}

// TestSMTPSettingsTestReportsFailureWhenUnconfigured drives the "server's
// actual response" requirement from the least fragile angle available in
// this package (no in-process SMTP server here — that lives in
// internal/mail's own tests): with nothing ever saved, deliver() (see
// internal/mail) refuses to even dial, and that refusal must surface as a
// clear failure, not a silent 200.
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
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("smtp test with nothing configured: status %d, want 502; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func decodeSMTPGet(resp *http.Response) (struct {
	Configured bool   `json:"configured"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
}, error,
) {
	var out struct {
		Configured bool   `json:"configured"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
	}
	err := json.NewDecoder(resp.Body).Decode(&out)
	return out, err
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
