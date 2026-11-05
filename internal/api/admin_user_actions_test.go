package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestAdminTemporaryPasswordEndsOtherSessions: the temporary password is
// shown once, uncached; the account's sessions end at once; the password
// signs in and forces a change; and it appears nowhere else — not in the
// log, the audit trail or the notice mail.
func TestAdminTemporaryPasswordEndsOtherSessions(t *testing.T) {
	s := apitest.New(t)
	logs := captureLog(t)
	admin, adminClient := adminWithFactor(t, s, "temp-admin@example.test")
	target := s.SeedUser("temp-target@example.test", "the old password 1")
	targetClient := s.LoginAs(target)

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "temporary-password"), nil)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("temporary password: status %d (%s), want 200", resp.StatusCode, raw)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q, want no-store", cc)
	}
	var body temporaryPasswordBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if !temporaryPasswordShape.MatchString(body.Password) || body.Notice != "sent" {
		t.Fatalf("body = %+v, want a temporary password and notice sent", body)
	}
	if d := time.Until(body.ExpiresAt); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("expires_at %v, want about 24 h from now", body.ExpiresAt)
	}

	if resp := requestJSON(t, targetClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the target's existing session after the action: status %d, want 401", resp.StatusCode)
	}
	if resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the admin's own session after the action: status %d, want 200", resp.StatusCode)
	}

	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": "temp-target@example.test", "password": "the old password 1",
	}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with the old password: status %d, want 401", resp.StatusCode)
	}
	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": "temp-target@example.test", "password": body.Password,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login with the temporary password: status %d, want 200", resp.StatusCode)
	}
	var mustChange bool
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT must_change_password FROM users WHERE id = $1`, target).Scan(&mustChange); err != nil {
		t.Fatal(err)
	}
	if !mustChange {
		t.Fatal("must_change_password is not set")
	}

	n, match, trail := auditEvents(t, s, "POST setTemporaryPassword", admin, target)
	if n != 1 || !match {
		t.Fatalf("audit: %d setTemporaryPassword events (instance-level, admin acting on the target: %v), want 1", n, match)
	}
	sent := s.Mail.Sent()
	if len(sent) != 1 || sent[0].To != "temp-target@example.test" {
		t.Fatalf("mails %+v, want one notice to the target", sent)
	}
	for where, text := range map[string]string{"log": logs.String(), "audit trail": trail, "notice mail": sent[0].Subject + sent[0].Body} {
		if strings.Contains(text, body.Password) {
			t.Fatalf("the %s carries the temporary password: %s", where, text)
		}
	}
}

// TestAdminResetSecondFactor: every factor goes, every session ends, and
// the password still works — the next sign-in must enrol a new factor.
func TestAdminResetSecondFactor(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "reset-admin@example.test")
	target := s.SeedUser("reset-target@example.test", "correct horse battery")
	s.SeedPasskey(target, "lost key")
	targetClient := s.LoginAs(target)

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "second-factor/reset"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset: status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var body noticeBody
	decodeInto(t, resp, &body)
	if body.Notice != "sent" {
		t.Fatalf("notice %q, want sent", body.Notice)
	}
	if resp := requestJSON(t, targetClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the target's session after the reset: status %d, want 401", resp.StatusCode)
	}
	var passkeys int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM passkeys WHERE user_id = $1`, target).Scan(&passkeys); err != nil {
		t.Fatal(err)
	}
	if passkeys != 0 {
		t.Fatalf("%d passkeys left, want 0", passkeys)
	}
	if n, match, _ := auditEvents(t, s, "POST resetSecondFactor", admin, target); n != 1 || !match {
		t.Fatalf("audit: %d resetSecondFactor events (match %v), want 1", n, match)
	}
}

// TestAdminEndSessionsKeepsTokens: ending the sessions signs the account
// out everywhere, but its API tokens keep working; they are managed per
// organization.
func TestAdminEndSessionsKeepsTokens(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "end-admin@example.test")
	org, tok := s.OrgWithToken(t)
	var owner uuid.UUID
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT user_id FROM org_memberships WHERE org_id = $1`, org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	ownerClient := s.LoginAs(owner)

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(owner, "sessions/revoke"), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("end sessions: status %d (%s), want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, ownerClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the owner's session: status %d, want 401", resp.StatusCode)
	}
	if resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()); resp.Code != http.StatusOK {
		t.Fatalf("the owner's token: status %d (%s), want 200", resp.Code, resp.Body)
	}
	if n, match, _ := auditEvents(t, s, "POST revokeUserSessions", admin, owner); n != 1 || !match {
		t.Fatalf("audit: %d revokeUserSessions events (match %v), want 1", n, match)
	}
	if len(s.Mail.Sent()) != 0 {
		t.Fatalf("ending sessions sent mail: %+v", s.Mail.Sent())
	}
}

// TestAdminDeactivateAndReactivate: a deactivated account cannot sign in
// and its person is told; reactivating lets the same password in again.
func TestAdminDeactivateAndReactivate(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "react-admin@example.test")
	target := s.SeedUser("react-target@example.test", "correct horse battery")
	login := func() int {
		resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
			"email": "react-target@example.test", "password": "correct horse battery",
		})
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "deactivate"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivate: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var body noticeBody
	decodeInto(t, resp, &body)
	if body.Notice != "sent" || len(s.Mail.Sent()) != 1 || s.Mail.Sent()[0].To != "react-target@example.test" {
		t.Fatalf("notice %q, mails %+v, want one notice to the target", body.Notice, s.Mail.Sent())
	}
	if code := login(); code != http.StatusUnauthorized {
		t.Fatalf("login while deactivated: status %d, want 401", code)
	}

	// Deactivating it again changes nothing and tells nobody.
	resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "deactivate"), nil)
	decodeInto(t, resp, &body)
	if resp.StatusCode != http.StatusOK || body.Notice != "skipped" || len(s.Mail.Sent()) != 1 {
		t.Fatalf("deactivating again: status %d, notice %q, mails %d; want 200, skipped, still 1", resp.StatusCode, body.Notice, len(s.Mail.Sent()))
	}
	// A reset link issued before the deactivation (here: written straight
	// into the table, since the forgot route sends a deactivated account
	// nothing) is void after the reactivation.
	const staleToken = "a-link-from-before-the-deactivation"
	if _, err := s.DB.Pool().Exec(t.Context(),
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at) VALUES (gen_random_uuid(), $1, sha256($2::bytea), now() + interval '1 hour')`,
		target, []byte(staleToken)); err != nil {
		t.Fatal(err)
	}

	// Deactivation disables the account and revokes its sessions in two
	// steps; a session that slipped between them ends on reactivation.
	survivor := s.LoginAs(target)
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE sessions SET revoked_at = NULL WHERE user_id = $1`, target); err != nil {
		t.Fatal(err)
	}

	resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "reactivate"), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reactivate: status %d (%s), want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	if code := login(); code != http.StatusOK {
		t.Fatalf("login after reactivation: status %d, want 200", code)
	}
	if resp := requestJSON(t, survivor, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a session from before the reactivation: status %d, want 401", resp.StatusCode)
	}
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/password/reset", map[string]string{"token": staleToken, "new_password": "a password from the old link"})
	if d := problemDetail(t, resp); resp.StatusCode != http.StatusBadRequest || d != "invalid_or_expired_link" {
		t.Fatalf("a reset link from before the deactivation: status %d detail %q, want 400 invalid_or_expired_link", resp.StatusCode, d)
	}
	if n, match, _ := auditEvents(t, s, "POST reactivateUser", admin, target); n != 1 || !match {
		t.Fatalf("audit: %d reactivateUser events (match %v), want 1", n, match)
	}
}

// TestAdminReactivateLeavesTheInvitationPending is backlog 7.82 (5): an
// invitee deactivated before accepting can be reactivated, and that gives
// the account no password — the Users list shows it invite-pending again —
// so the invitation still works: accepting it sets the invitee's own
// password.
func TestAdminReactivateLeavesTheInvitationPending(t *testing.T) {
	s := apitest.New(t)
	_, adminClient := adminWithFactor(t, s, "pending-react-admin@example.test")
	owner := s.SeedUser("pending-react-owner@example.test", "correct horse battery")
	s.SeedOrg("pending-react-org", owner)
	const email = "pending-react@example.test"
	invitee := seedInvitePending(t, s, email)
	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/pending-react-org/invites",
		map[string]any{"email": email, "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invite: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()
	token := extractInviteToken(t, s)

	// The account page says the invitation is pending in either state, so
	// the SPA offers a deactivated invitee nothing but reactivation.
	for _, step := range []struct{ action, state string }{{"deactivate", "deactivated"}, {"reactivate", "invite_pending"}} {
		resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(invitee, step.action), nil)
		if resp.StatusCode >= http.StatusBadRequest {
			t.Fatalf("%s: status %d (%s)", step.action, resp.StatusCode, mustReadBody(t, resp))
		}
		_ = resp.Body.Close()
		resp = requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+adminUserPath(invitee, ""), nil)
		var detail adminUserDetailBody
		decodeInto(t, resp, &detail)
		if detail.User.State != step.state || !detail.User.InvitePending {
			t.Fatalf("after %s: state %q, invite_pending %v; want %s, true", step.action, detail.User.State, detail.User.InvitePending, step.state)
		}
	}

	resp = requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+adminUsersPath+"?q=pending-react%40", nil)
	var list adminUserListBody
	decodeInto(t, resp, &list)
	if len(list.Users) != 1 || list.Users[0].ID != invitee.String() || list.Users[0].State != "invite_pending" {
		t.Fatalf("the reactivated invitee in the list = %+v, want one invite_pending row", list.Users)
	}

	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Invitee", "password": "the invitee's own password",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accepting after the reactivation: status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()
	if resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]string{
		"email": email, "password": "the invitee's own password",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login with the password set by accepting: status %d, want 200", resp.StatusCode)
	}
}

// TestAdminActionOnDeactivatedAccountSkipsNotice: an action on a
// deactivated account happens, but its person is not mailed about it.
func TestAdminActionOnDeactivatedAccountSkipsNotice(t *testing.T) {
	s := apitest.New(t)
	_, adminClient := adminWithFactor(t, s, "skip-admin@example.test")
	target := s.SeedUser("skip-target@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, target); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "temporary-password"), nil)
	var tmp temporaryPasswordBody
	decodeInto(t, resp, &tmp)
	if resp.StatusCode != http.StatusOK || tmp.Notice != "skipped" || tmp.Password == "" {
		t.Fatalf("temporary password: status %d, notice %q; want 200, a password and skipped", resp.StatusCode, tmp.Notice)
	}
	resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "second-factor/reset"), nil)
	var body noticeBody
	decodeInto(t, resp, &body)
	if resp.StatusCode != http.StatusOK || body.Notice != "skipped" {
		t.Fatalf("second factor reset: status %d, notice %q; want 200 and skipped", resp.StatusCode, body.Notice)
	}
	if sent := s.Mail.Sent(); len(sent) != 0 {
		t.Fatalf("mails %+v, want none", sent)
	}
}

// TestAdminDeactivateNoticeWithoutMail: without outbound mail the action
// still happens and the admin learns that the person was not told.
func TestAdminDeactivateNoticeWithoutMail(t *testing.T) {
	s := apitest.New(t)
	_, adminClient := adminWithFactor(t, s, "nomail-admin@example.test")
	target := s.SeedUser("nomail-target@example.test", "correct horse battery")
	s.Mail.SetConfigured(false)

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "temporary-password"), nil)
	var tmp temporaryPasswordBody
	decodeInto(t, resp, &tmp)
	if resp.StatusCode != http.StatusOK || tmp.Notice != "not_configured" || tmp.Password == "" {
		t.Fatalf("temporary password without mail: status %d, body %+v", resp.StatusCode, tmp)
	}

	resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(target, "deactivate"), nil)
	var body noticeBody
	decodeInto(t, resp, &body)
	if resp.StatusCode != http.StatusOK || body.Notice != "not_configured" {
		t.Fatalf("deactivate without mail: status %d, notice %q", resp.StatusCode, body.Notice)
	}
}
