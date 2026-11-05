package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

const changePasswordPath = "/api/v1/auth/password"

// captureLog routes the default slog logger into a buffer for the rest of
// the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestChangePasswordEndpoint: the owner changes their own password. The
// current session keeps working, every other session of the account ends,
// the forced-change flag is cleared, and neither password shows up in the
// response, the log or the audit trail.
func TestChangePasswordEndpoint(t *testing.T) {
	const (
		oldPassword = "the old password 1"
		newPassword = "the new password 22"
	)
	s := apitest.New(t)
	logs := captureLog(t)
	uid := s.SeedUser("change-own@example.test", oldPassword)
	s.SeedPasskey(uid, "key")
	markMustChange(t, s, uid)
	c := s.LoginAs(uid)
	other := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+changePasswordPath, map[string]string{
		"current_password": oldPassword, "new_password": newPassword,
	})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change password: status %d (%s), want 204", resp.StatusCode, body)
	}
	for _, secret := range []string{oldPassword, newPassword} {
		if strings.Contains(body, secret) {
			t.Fatalf("the response carries a password: %q", body)
		}
	}

	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the changing session after the change: GET /orgs status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, other, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("another session after the change: status %d, want 401", resp.StatusCode)
	}

	var mustChange bool
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT must_change_password FROM users WHERE id = $1`, uid).Scan(&mustChange); err != nil {
		t.Fatal(err)
	}
	if mustChange {
		t.Fatal("must_change_password is still set after the change")
	}

	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": "change-own@example.test", "password": oldPassword,
	}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with the old password: status %d, want 401", resp.StatusCode)
	}
	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": "change-own@example.test", "password": newPassword,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login with the new password: status %d, want 200", resp.StatusCode)
	}

	var (
		events  int
		orgless bool
		trail   string
	)
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT count(*), bool_and(org_id IS NULL AND actor_id = $1 AND object_id = $1::text),
		        coalesce(string_agg(summary || ' ' || meta::text, ' '), '')
		   FROM audit_events WHERE action = 'POST changePassword'`, uid).Scan(&events, &orgless, &trail); err != nil {
		t.Fatal(err)
	}
	if events != 1 || !orgless {
		t.Fatalf("audit: %d changePassword events (instance-level, actor and object the user: %v), want 1", events, orgless)
	}
	for _, secret := range []string{oldPassword, newPassword} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the server log carries a password: %s", logs)
		}
		if strings.Contains(trail, secret) {
			t.Fatalf("the audit trail carries a password: %s", trail)
		}
	}
}

// TestChangePasswordRefusals: the route's gate and the service's refusals,
// each with the status and detail the SPA reads.
func TestChangePasswordRefusals(t *testing.T) {
	const password = "the current password"
	s := apitest.New(t)
	logs := captureLog(t)
	uid := s.SeedUser("change-refused@example.test", password)
	s.SeedPasskey(uid, "key")

	for _, tc := range []struct {
		name       string
		client     *http.Client
		current    string
		next       string
		wantStatus int
		wantDetail string
	}{
		{"wrong current password", s.LoginAs(uid), "not the password", "a brand new password", http.StatusBadRequest, "invalid_current_password"},
		{"too short", s.LoginAs(uid), password, "elevenchars", http.StatusUnprocessableEntity, "password_too_short"},
		{"same as the current one", s.LoginAs(uid), password, password, http.StatusUnprocessableEntity, "same_password"},
		{"second factor pending", s.LoginAsPending(uid), password, "a brand new password", http.StatusForbidden, ""},
		{"signed out", s.Client(), password, "a brand new password", http.StatusUnauthorized, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, tc.client, s.HTTP.URL+changePasswordPath, map[string]string{
				"current_password": tc.current, "new_password": tc.next,
			})
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status %d (%s), want %d", resp.StatusCode, mustReadBody(t, resp), tc.wantStatus)
			}
			if d := problemDetail(t, resp); tc.wantDetail != "" && d != tc.wantDetail {
				t.Fatalf("detail %q, want %q", d, tc.wantDetail)
			}
		})
	}

	// An API token is refused by requirePasswordChangeSession, not here:
	// this route resolves no bearer credential at all, so a request with a
	// token would be a plain 401 whatever the gate did. The token principal
	// is covered in must_change_gate_internal_test.go.

	t.Run("a stale factor", func(t *testing.T) {
		c := s.LoginAs(uid)
		stale(t, s, uid)
		resp := postJSON(t, c, s.HTTP.URL+changePasswordPath, map[string]string{
			"current_password": password, "new_password": "a brand new password",
		})
		if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != "step_up_required" {
			t.Fatalf("status %d, want 403 step_up_required", resp.StatusCode)
		}
	})

	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": "change-refused@example.test", "password": password,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("a refused change altered the password: login status %d", resp.StatusCode)
	}
	if strings.Contains(logs.String(), "a brand new password") || strings.Contains(logs.String(), password) {
		t.Fatalf("the server log carries a password: %s", logs)
	}
}

// TestLoginRefusesExpiredTemporaryPasswordOverHTTP: the expiry travels from
// the users row through the auth store to Login; a live temporary password
// still signs in.
func TestLoginRefusesExpiredTemporaryPasswordOverHTTP(t *testing.T) {
	const password = "the temporary password"
	s := apitest.New(t)
	uid := s.SeedUser("temp-expiry@example.test", password)
	login := func() int {
		resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
			"email": "temp-expiry@example.test", "password": password,
		})
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	markMustChange(t, s, uid)
	if code := login(); code != http.StatusOK {
		t.Fatalf("login with a live temporary password: status %d, want 200", code)
	}
	if _, err := s.DB.Pool().Exec(t.Context(),
		`UPDATE users SET temp_password_expires_at = now() - interval '1 second' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if code := login(); code != http.StatusUnauthorized {
		t.Fatalf("login with an expired temporary password: status %d, want 401", code)
	}
}

// TestChangePasswordRefusesExpiredTemporaryPassword: a session that is
// still signed in (a passkey sign-in never saw the password) cannot use an
// expired temporary password as the current one. The refusal has its own
// detail, so the SPA can tell the person to ask an administrator for a new
// one; the caller is authenticated, so the detail gives nothing away. A
// wrong password still gets the generic detail.
func TestChangePasswordRefusesExpiredTemporaryPassword(t *testing.T) {
	const password = "the temporary password"
	s := apitest.New(t)
	logs := captureLog(t)
	uid := s.SeedUser("temp-expired-change@example.test", password)
	markMustChange(t, s, uid)
	if _, err := s.DB.Pool().Exec(t.Context(),
		`UPDATE users SET temp_password_expires_at = now() - interval '1 second' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	c := s.LoginAs(uid)

	for _, tc := range []struct {
		current, wantDetail string
	}{
		{password, "temporary_password_expired"},
		{"not the password", "invalid_current_password"},
	} {
		resp := postJSON(t, c, s.HTTP.URL+changePasswordPath, map[string]string{
			"current_password": tc.current, "new_password": "a brand new password",
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
		if d := problemDetail(t, resp); d != tc.wantDetail {
			t.Fatalf("detail %q, want %q", d, tc.wantDetail)
		}
	}
	if strings.Contains(logs.String(), password) {
		t.Fatalf("the server log carries a password: %s", logs)
	}
}
