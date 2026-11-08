package api_test

import (
	"bytes"
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
