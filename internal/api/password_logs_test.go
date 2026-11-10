package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// captureLog routes the default slog logger into a buffer for the rest of
// the test. The API package logs through the package-level slog functions
// (Deps has no logger), so the default is swapped; no test in internal/api
// runs t.Parallel, which this relies on.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestChangePasswordLogsNoPassword: no password of a change-password
// request reaches the server log, whether the request is refused or
// succeeds. The refusals log nothing and a working audit store keeps the
// success quiet too, so on its own an absence check would pass over an empty
// log. The server therefore audits through a store that always fails: the
// successful change then writes "audit: failed to record event" through the
// same default logger, and the test requires that line, which proves the
// capture is live before it looks for a password.
func TestChangePasswordLogsNoPassword(t *testing.T) {
	const (
		current  = "the current password A"
		next1    = "a brand new password 1"
		next2    = "a brand new password 2"
		tempPass = "the temporary password"
		tooShort = "elevenchars"
		wrong    = "not the password"
	)
	s := apitest.New(t, apitest.WithFailingAudit())
	logs := captureLog(t)

	// User A: refusals first, then a successful change.
	a := s.SeedUser("change-log-a@example.test", current)
	s.SeedPasskey(a, "key")
	ca := s.LoginAs(a)
	change := func(c *http.Client, cur, next string) *http.Response {
		return postJSON(t, c, s.HTTP.URL+changePasswordPath, map[string]string{
			"current_password": cur, "new_password": next,
		})
	}
	for _, tc := range []struct {
		name, cur, next string
		wantStatus      int
	}{
		{"wrong current password", wrong, next1, http.StatusBadRequest},
		{"too short", current, tooShort, http.StatusUnprocessableEntity},
		{"same as the current one", current, current, http.StatusUnprocessableEntity},
	} {
		if resp := change(ca, tc.cur, tc.next); resp.StatusCode != tc.wantStatus {
			t.Fatalf("%s: status %d (%s), want %d", tc.name, resp.StatusCode, mustReadBody(t, resp), tc.wantStatus)
		}
	}
	if resp := change(ca, current, next2); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change password: status %d (%s), want 204", resp.StatusCode, mustReadBody(t, resp))
	}

	// User B: an expired temporary password is refused as the current one.
	b := s.SeedUser("change-log-b@example.test", tempPass)
	markMustChange(t, s, b)
	if _, err := s.DB.Pool().Exec(t.Context(),
		`UPDATE users SET temp_password_expires_at = now() - interval '1 second' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	resp := change(s.LoginAs(b), tempPass, next1)
	if resp.StatusCode != http.StatusBadRequest || problemDetail(t, resp) != "temporary_password_expired" {
		t.Fatalf("expired temporary password: status %d, want 400 temporary_password_expired", resp.StatusCode)
	}

	got := logs.String()
	for _, line := range []string{"audit: failed to record event", "POST changePassword"} {
		if !strings.Contains(got, line) {
			t.Fatalf("the captured log lacks %q, so the absence check below would be vacuous: %q", line, got)
		}
	}
	for _, secret := range []string{current, next1, next2, tempPass, tooShort, wrong} {
		if strings.Contains(got, secret) {
			t.Fatalf("the server log carries %q: %s", secret, got)
		}
	}
}

// requireCapturedAuditFailure fails unless the captured log holds the line a
// failing audit store makes the request write for action, so the absence
// checks that follow cannot pass over an empty log.
func requireCapturedAuditFailure(t *testing.T, logs *bytes.Buffer, action string) string {
	t.Helper()
	got := logs.String()
	for _, line := range []string{"audit: failed to record event", action} {
		if !strings.Contains(got, line) {
			t.Fatalf("the captured log lacks %q, so the absence check below would be vacuous: %q", line, got)
		}
	}
	return got
}

// TestResetLogsNoToken: neither the reset token nor a password of the reset
// request reaches the server log, whether the request is refused or
// succeeds. Like TestChangePasswordLogsNoPassword, the server audits through
// a store that always fails, so the successful reset writes a log line and
// the capture is proven live before the absence check.
func TestResetLogsNoToken(t *testing.T) {
	const (
		oldPassword = "the old password 1"
		newPassword = "the new password 22"
		reusedWith  = "yet another password"
		tooShort    = "short"
	)
	s := apitest.New(t, apitest.WithFailingAudit())
	logs := captureLog(t)
	const email = "reset-log@example.test"
	s.SeedPasskey(s.SeedUser(email, oldPassword), "my key")

	if resp := postJSON(t, s.Client(), s.HTTP.URL+forgotPath, map[string]string{"email": email}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forgot: status %d", resp.StatusCode)
	}
	token := waitForResetMail(t, s, email)
	reset := func(password string) *http.Response {
		return postJSON(t, s.Client(), s.HTTP.URL+resetPath, map[string]string{"token": token, "new_password": password})
	}
	if resp := reset(tooShort); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a short password: status %d, want 422", resp.StatusCode)
	}
	if resp := reset(newPassword); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset: status %d (%s), want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := reset(reusedWith); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("the same link again: status %d, want 400", resp.StatusCode)
	}

	got := requireCapturedAuditFailure(t, logs, "POST resetPassword")
	for _, secret := range []string{token, oldPassword, newPassword, reusedWith, tooShort} {
		if strings.Contains(got, secret) {
			t.Fatalf("the server log carries %q: %s", secret, got)
		}
	}
}

// TestAdminTemporaryPasswordLogsNoPassword: the temporary password an admin
// sets reaches the response and nothing else, not the server log. The audit
// store fails on purpose, as in TestResetLogsNoToken, so the log has a line
// to prove the capture before the absence check.
func TestAdminTemporaryPasswordLogsNoPassword(t *testing.T) {
	s := apitest.New(t, apitest.WithFailingAudit())
	logs := captureLog(t)
	_, adminClient := adminWithFactor(t, s, "temp-log-admin@example.test")
	target := s.SeedUser("temp-log-target@example.test", "the old password 1")

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "temporary-password"), nil)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("temporary password: status %d (%s), want 200", resp.StatusCode, raw)
	}
	var body temporaryPasswordBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if !temporaryPasswordShape.MatchString(body.Password) {
		t.Fatalf("body = %+v, want a temporary password", body)
	}

	got := requireCapturedAuditFailure(t, logs, "POST setTemporaryPassword")
	for _, secret := range []string{body.Password, "the old password 1"} {
		if strings.Contains(got, secret) {
			t.Fatalf("the server log carries %q: %s", secret, got)
		}
	}
}
