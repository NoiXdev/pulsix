package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

const (
	forgotPath         = "/api/v1/auth/password/forgot"
	resetPath          = "/api/v1/auth/password/reset"
	resetAvailablePath = "/api/v1/auth/password/reset-available"
)

// resetTokenInMail finds the token of a reset link: in the fragment, never
// in the query.
var resetTokenInMail = regexp.MustCompile(`/reset-password#token=([A-Za-z0-9_-]+)`)

// waitForResetMail waits for the reset mail to reach to (the forgot route
// hands it to the mailer in the background) and returns its token.
func waitForResetMail(t *testing.T, s *apitest.Server, to string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range s.Mail.Sent() {
			if m.To == to {
				match := resetTokenInMail.FindStringSubmatch(m.Body)
				if match == nil || strings.Contains(m.Body, "?token=") {
					t.Fatalf("the reset mail carries no fragment link: %q", m.Body)
				}
				return match[1]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no reset mail to %s", to)
	return ""
}

func TestResetAvailable(t *testing.T) {
	s := apitest.New(t)
	get := func() bool {
		t.Helper()
		resp := requestJSON(t, s.Client(), http.MethodGet, s.HTTP.URL+resetAvailablePath, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
		}
		var body struct {
			Available bool `json:"available"`
		}
		decodeInto(t, resp, &body)
		return body.Available
	}
	if !get() {
		t.Fatal("available = false with mail configured")
	}
	s.Mail.SetConfigured(false)
	if get() {
		t.Fatal("available = true without mail")
	}
}

// TestForgotPasswordConstantShape: the answer never tells whether an
// address has an account, or what state it is in.
func TestForgotPasswordConstantShape(t *testing.T) {
	s := apitest.New(t)
	s.SeedUser("forgot-existing@example.test", "correct horse battery")
	deactivated := s.SeedUser("forgot-deactivated@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, deactivated); err != nil {
		t.Fatal(err)
	}
	seedInvitePending(t, s, "forgot-pending@example.test")

	var first string
	for _, email := range []string{
		"forgot-existing@example.test",
		"forgot-unknown@example.test",
		"forgot-deactivated@example.test",
		"forgot-pending@example.test",
	} {
		resp := postJSON(t, s.Client(), s.HTTP.URL+forgotPath, map[string]string{"email": email})
		body := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status %d (%s), want 202", email, resp.StatusCode, body)
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Fatalf("%s: body %q differs from %q", email, body, first)
		}
	}
	// Drain the background work of all four requests, so a late mail or
	// row would be counted.
	s.Deps.Accounts.Wait()
	waitForResetMail(t, s, "forgot-existing@example.test")
	if sent := s.Mail.Sent(); len(sent) != 1 {
		t.Fatalf("mails %+v, want only the existing account's", sent)
	}
	var rows int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM password_reset_tokens`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d token rows, want 1", rows)
	}
}

// TestForgotPasswordStillAnswers202WhenTheRequestIsDropped: once the
// accounts service has shut down it starts no more background work, and a
// forgot request is still answered exactly like any other; no mail goes out.
func TestForgotPasswordStillAnswers202WhenTheRequestIsDropped(t *testing.T) {
	s := apitest.New(t)
	s.SeedUser("forgot-dropped@example.test", "correct horse battery")
	if err := s.Deps.Accounts.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, s.Client(), s.HTTP.URL+forgotPath, map[string]string{"email": "forgot-dropped@example.test"})
	body := mustReadBody(t, resp)
	// The usual body: an empty object, carrying only huma's $schema link.
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", body, err)
	}
	delete(fields, "$schema")
	if resp.StatusCode != http.StatusAccepted || len(fields) != 0 {
		t.Fatalf("status %d body %q, want 202 and an empty object", resp.StatusCode, body)
	}
	s.Deps.Accounts.Wait()
	if sent := s.Mail.Sent(); len(sent) != 0 {
		t.Fatalf("mails %+v, want none: the request was dropped", sent)
	}
}

// TestForgotPasswordRateLimit: every request counts, whatever it asked
// for; the 11th from one address within 15 minutes is refused, another
// address keeps its own budget.
func TestForgotPasswordRateLimit(t *testing.T) {
	s := apitest.New(t, apitest.WithTrustedProxies(loopbackTrusted()))
	// The accepted requests start background work; it must be done before
	// the test's database goes away.
	t.Cleanup(s.Deps.Accounts.Wait)
	for i := range 10 {
		resp, err := postFrom(s.HTTP.URL+forgotPath, addressA, map[string]string{"email": "nobody@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("request %d: status %d, want 202", i+1, resp.StatusCode)
		}
	}
	resp, err := postFrom(s.HTTP.URL+forgotPath, addressA, map[string]string{"email": "nobody@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th request: status %d, want 429", resp.StatusCode)
	}
	if ra := retryAfterOf(t, resp); ra > 15*60 {
		t.Fatalf("Retry-After %d s, want at most the 15 min window", ra)
	}
	other, err := postFrom(s.HTTP.URL+forgotPath, addressB, map[string]string{"email": "nobody@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = other.Body.Close()
	if other.StatusCode != http.StatusAccepted {
		t.Fatalf("another address: status %d, want 202", other.StatusCode)
	}
}

// TestResetDoesNotSignIn: a reset sets the password and ends every session,
// but signs nobody in; the next sign-in still needs the second factor. The
// token does not appear in the audit trail (the log is checked by
// TestResetLogsNoToken, which needs a failing audit store to have a line to
// capture).
func TestResetDoesNotSignIn(t *testing.T) {
	s := apitest.New(t)
	const email = "reset-me@example.test"
	uid := s.SeedUser(email, "the old password 1")
	s.SeedPasskey(uid, "my key")
	oldSession := s.LoginAs(uid)

	if resp := postJSON(t, s.Client(), s.HTTP.URL+forgotPath, map[string]string{"email": email}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forgot: status %d", resp.StatusCode)
	}
	token := waitForResetMail(t, s, email)

	if resp := postJSON(t, s.Client(), s.HTTP.URL+resetPath, map[string]string{"token": token, "new_password": "short"}); resp.StatusCode != http.StatusUnprocessableEntity || problemDetail(t, resp) != "password_too_short" {
		t.Fatalf("a short password: status %d, want 422 password_too_short", resp.StatusCode)
	}

	resp := postJSON(t, s.Client(), s.HTTP.URL+resetPath, map[string]string{"token": token, "new_password": "the new password 22"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset: status %d (%s), want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	if c := resp.Header.Values("Set-Cookie"); len(c) != 0 {
		t.Fatalf("the reset set cookies: %v", c)
	}
	if resp := requestJSON(t, oldSession, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the old session after the reset: status %d, want 401", resp.StatusCode)
	}

	again := postJSON(t, s.Client(), s.HTTP.URL+resetPath, map[string]string{"token": token, "new_password": "yet another password"})
	if d := problemDetail(t, again); again.StatusCode != http.StatusBadRequest || d != "invalid_or_expired_link" {
		t.Fatalf("the same link again: status %d detail %q, want 400 invalid_or_expired_link", again.StatusCode, d)
	}

	login := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": email, "password": "the new password 22"})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login with the new password: status %d", login.StatusCode)
	}
	var body struct {
		MFARequired bool `json:"mfa_required"`
	}
	decodeInto(t, login, &body)
	if !body.MFARequired {
		t.Fatal("the sign-in after a reset skipped the second factor")
	}

	n, match, trail := auditEvents(t, s, "POST resetPassword", uid, uid)
	if n != 1 || !match {
		t.Fatalf("audit: %d resetPassword events (instance-level, the user acting on themselves: %v), want 1", n, match)
	}
	if strings.Contains(trail, token) {
		t.Fatal("the audit trail carries the reset token")
	}
}

// TestTemporaryPasswordLocksOutAResetLink: an administrator who sets a
// temporary password suspects someone else has the account, maybe the
// mailbox too. A link mailed before stops working, and asking for a new
// one sends none while the change is pending, so neither undoes the
// lock-out with a password of the holder's choosing.
func TestTemporaryPasswordLocksOutAResetLink(t *testing.T) {
	s := apitest.New(t)
	const email = "locked-out@example.test"
	target := s.SeedUser(email, "the old password 1")
	_, adminClient := adminWithFactor(t, s, "lockout-admin@example.test")

	if resp := postJSON(t, s.Client(), s.HTTP.URL+forgotPath, map[string]string{"email": email}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forgot: status %d", resp.StatusCode)
	}
	earlier := waitForResetMail(t, s, email)

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "temporary-password"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("temporary password: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()

	again := postJSON(t, s.Client(), s.HTTP.URL+resetPath, map[string]string{"token": earlier, "new_password": "the holder's password"})
	if d := problemDetail(t, again); again.StatusCode != http.StatusBadRequest || d != "invalid_or_expired_link" {
		t.Fatalf("the earlier link: status %d detail %q, want 400 invalid_or_expired_link", again.StatusCode, d)
	}

	mailsBefore := len(s.Mail.Sent())
	if resp := postJSON(t, s.Client(), s.HTTP.URL+forgotPath, map[string]string{"email": email}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forgot again: status %d, want 202", resp.StatusCode)
	}
	s.Deps.Accounts.Wait()
	for _, m := range s.Mail.Sent()[mailsBefore:] {
		if resetTokenInMail.MatchString(m.Body) {
			t.Fatal("forgot mailed a reset link while the password change is pending")
		}
	}
}

func TestResetRefusesUnknownToken(t *testing.T) {
	s := apitest.New(t)
	resp := postJSON(t, s.Client(), s.HTTP.URL+resetPath, map[string]string{"token": "made-up", "new_password": "the new password 22"})
	if d := problemDetail(t, resp); resp.StatusCode != http.StatusBadRequest || d != "invalid_or_expired_link" {
		t.Fatalf("status %d detail %q, want 400 invalid_or_expired_link", resp.StatusCode, d)
	}
	var events int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action = 'POST resetPassword'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("%d audit events for a refused reset, want 0", events)
	}
}

func TestAdminSendResetLink(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "link-admin@example.test")
	target := s.SeedUser("link-target@example.test", "correct horse battery")

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "password-reset-link"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var body noticeBody
	decodeInto(t, resp, &body)
	if body.Notice != "sent" {
		t.Fatalf("notice %q, want sent", body.Notice)
	}
	token := waitForResetMail(t, s, "link-target@example.test")
	n, match, trail := auditEvents(t, s, "POST sendPasswordResetLink", admin, target)
	if n != 1 || !match || !strings.Contains(trail, "sent a password reset link") {
		t.Fatalf("audit: %d sendPasswordResetLink events (match %v, trail %q), want 1 saying it was sent", n, match, trail)
	}
	if strings.Contains(trail, token) {
		t.Fatal("the audit trail carries the reset token")
	}
	// The current password keeps working until the link is used.
	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": "link-target@example.test", "password": "correct horse battery",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the current password after the link was sent: status %d, want 200", resp.StatusCode)
	}

	t.Run("refusals", func(t *testing.T) {
		resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(admin, "password-reset-link"), nil)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "not_on_own_account" {
			t.Errorf("own account: status %d detail %q, want 409 not_on_own_account", resp.StatusCode, d)
		}
		pending := seedInvitePending(t, s, "link-pending@example.test")
		resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(pending, "password-reset-link"), nil)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "invite_pending" {
			t.Errorf("invite pending: status %d detail %q, want 409 invite_pending", resp.StatusCode, d)
		}
		if resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(uuid.Must(uuid.NewV7()), "password-reset-link"), nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown account: status %d, want 404", resp.StatusCode)
		}
		owner := s.SeedUser("link-owner@example.test", "correct horse battery")
		if resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+adminUserPath(target, "password-reset-link"), nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("not an instance admin: status %d, want 403", resp.StatusCode)
		}
		stale(t, s, admin)
		resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "password-reset-link"), nil)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusForbidden || d != "step_up_required" {
			t.Errorf("stale factor: status %d detail %q, want 403 step_up_required", resp.StatusCode, d)
		}
	})
}

// TestAdminSendResetLinkOutcomes: the answer and the audit summary say what
// happened — no link for a deactivated account, a link whose mail failed.
func TestAdminSendResetLinkOutcomes(t *testing.T) {
	s := apitest.New(t)
	logs := captureLog(t)
	admin, adminClient := adminWithFactor(t, s, "outcome-admin@example.test")
	deactivated := s.SeedUser("outcome-off@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, deactivated); err != nil {
		t.Fatal(err)
	}
	failing := s.SeedUser("outcome-fail@example.test", "correct horse battery")

	send := func(target uuid.UUID) string {
		t.Helper()
		resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "password-reset-link"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
		}
		var body noticeBody
		decodeInto(t, resp, &body)
		return body.Notice
	}

	if notice := send(deactivated); notice != "skipped" {
		t.Fatalf("deactivated: notice %q, want skipped", notice)
	}
	_, _, trail := auditEvents(t, s, "POST sendPasswordResetLink", admin, deactivated)
	if strings.Contains(trail, "sent a password reset link") || !strings.Contains(trail, "deactivated") {
		t.Fatalf("deactivated: audit trail %q claims a link or misses why", trail)
	}
	var rows int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, deactivated).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d tokens for a deactivated account, want 0", rows)
	}

	s.Mail.FailWith(errors.New("smtp down"))
	if notice := send(failing); notice != "failed" {
		t.Fatalf("failed send: notice %q, want failed", notice)
	}
	_, _, trail = auditEvents(t, s, "POST sendPasswordResetLink", admin, failing)
	if strings.Contains(trail, "sent a password reset link") || !strings.Contains(trail, "failed") {
		t.Fatalf("failed send: audit trail %q", trail)
	}
	if !strings.Contains(logs.String(), "smtp down") || strings.Contains(logs.String(), "token=") {
		t.Fatal("the failure was not logged, or the log carries the link")
	}
}

func TestAdminSendResetLinkWithoutMail(t *testing.T) {
	s := apitest.New(t)
	_, adminClient := adminWithFactor(t, s, "nolink-admin@example.test")
	target := s.SeedUser("nolink-target@example.test", "correct horse battery")
	s.Mail.SetConfigured(false)

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "password-reset-link"), nil)
	if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "mail_not_configured" {
		t.Fatalf("status %d detail %q, want 409 mail_not_configured", resp.StatusCode, d)
	}
	var rows int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM password_reset_tokens`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || len(s.Mail.Sent()) != 0 {
		t.Fatalf("token rows %d, mails %d; want none", rows, len(s.Mail.Sent()))
	}
}
