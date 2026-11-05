package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db/gen"
)

const adminUsersPath = "/api/v1/admin/users"

var temporaryPasswordShape = regexp.MustCompile(`^[a-hj-km-np-z2-9]{5}(-[a-hj-km-np-z2-9]{5}){3}$`)

type adminUserBody struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	DisplayName   string     `json:"display_name"`
	InstanceAdmin bool       `json:"instance_admin"`
	State         string     `json:"state"`
	OrgCount      int        `json:"org_count"`
	LastSignInAt  *time.Time `json:"last_sign_in_at"`
}

type adminUserListBody struct {
	Users      []adminUserBody `json:"users"`
	NextCursor string          `json:"next_cursor"`
}

type adminUserDetailBody struct {
	User struct {
		adminUserBody
		CreatedAt             time.Time  `json:"created_at"`
		DisabledAt            *time.Time `json:"disabled_at"`
		MustChangePassword    bool       `json:"must_change_password"`
		TempPasswordExpiresAt *time.Time `json:"temp_password_expires_at"`
		Memberships           []struct {
			OrgID         string `json:"org_id"`
			Slug          string `json:"slug"`
			Name          string `json:"name"`
			Role          string `json:"role"`
			ActorIsMember bool   `json:"actor_is_member"`
		} `json:"memberships"`
		Factors struct {
			TOTP              bool `json:"totp"`
			Passkeys          int  `json:"passkeys"`
			RecoveryCodesLeft int  `json:"recovery_codes_left"`
		} `json:"factors"`
		ActiveSessions int `json:"active_sessions"`
	} `json:"user"`
}

type temporaryPasswordBody struct {
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
	Notice    string    `json:"notice"`
}

type noticeBody struct {
	Notice string `json:"notice"`
}

// adminWithFactor seeds an instance admin who carries a passkey, so the
// step-up applies to them, and returns a client with a fresh factor.
func adminWithFactor(t *testing.T, s *apitest.Server, email string) (uuid.UUID, *http.Client) {
	t.Helper()
	admin := s.SeedUser(email, "correct horse battery")
	makeInstanceAdmin(t, s, admin)
	s.SeedPasskey(admin, "admin key")
	return admin, s.LoginAs(admin)
}

// seedInvitePending seeds an account an invitation created but nobody
// accepted: it has no password.
func seedInvitePending(t *testing.T, s *apitest.Server, email string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateUser(context.Background(), gen.CreateUserParams{ID: id, Email: email, DisplayName: email}); err != nil {
		t.Fatal(err)
	}
	return id
}

func adminUserPath(id uuid.UUID, action string) string {
	p := adminUsersPath + "/" + id.String()
	if action != "" {
		p += "/" + action
	}
	return p
}

// mintToken creates an owner-role API token in the organization slug on
// behalf of the client's account and returns it.
func mintToken(t *testing.T, s *apitest.Server, c *http.Client, slug string) string {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/"+slug+"/tokens", map[string]any{"name": "ci", "role": "owner"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Token string `json:"token"`
	}
	decodeInto(t, resp, &created)
	return created.Token
}

// auditEvents returns how many events of action exist, whether all of them
// are instance-level with actor and object as given, and their summaries
// and metadata as one string.
func auditEvents(t *testing.T, s *apitest.Server, action string, actor, object uuid.UUID) (int, bool, string) {
	t.Helper()
	var (
		n     int
		match *bool
		trail string
	)
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT count(*), bool_and(org_id IS NULL AND actor_id = $2 AND object_type = 'user' AND object_id = $3::text),
		        coalesce(string_agg(summary || ' ' || meta::text, ' '), '')
		   FROM audit_events WHERE action = $1`, action, actor, object).Scan(&n, &match, &trail); err != nil {
		t.Fatal(err)
	}
	return n, match != nil && *match, trail
}

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

	for _, action := range []string{"deactivate", "reactivate"} {
		resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(invitee, action), nil)
		if resp.StatusCode >= http.StatusBadRequest {
			t.Fatalf("%s: status %d (%s)", action, resp.StatusCode, mustReadBody(t, resp))
		}
		_ = resp.Body.Close()
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

// TestAdminUserRoutesRequireInstanceAdminAndStepUp: every route is for an
// instance admin's cookie session; every action also needs a fresh factor,
// the reads do not.
func TestAdminUserRoutesRequireInstanceAdminAndStepUp(t *testing.T) {
	s := apitest.New(t)
	admin, _ := adminWithFactor(t, s, "gate-admin@example.test")
	target := s.SeedUser("gate-target@example.test", "correct horse battery")
	owner := s.SeedUser("gate-owner@example.test", "correct horse battery")
	org := s.SeedOrg("gate-org", owner)
	s.AddMember(org, admin, "owner")
	tok := mintToken(t, s, s.LoginAs(admin), "gate-org")

	reads := []string{adminUsersPath, adminUserPath(target, "")}
	actions := []string{
		adminUserPath(target, "temporary-password"),
		adminUserPath(target, "second-factor/reset"),
		adminUserPath(target, "sessions/revoke"),
		adminUserPath(target, "reactivate"),
		adminUserPath(target, "password-reset-link"),
		adminUserPath(target, "deactivate"),
	}

	t.Run("an org owner who is not an instance admin", func(t *testing.T) {
		c := s.LoginAs(owner)
		for _, p := range reads {
			if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+p, nil); resp.StatusCode != http.StatusForbidden {
				t.Errorf("GET %s: status %d, want 403", p, resp.StatusCode)
			}
		}
		for _, p := range actions {
			if resp := postJSON(t, c, s.HTTP.URL+p, nil); resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s: status %d, want 403", p, resp.StatusCode)
			}
		}
	})

	// The bearer credential is resolved only on the org-scoped routes, so on
	// these instance-level ones a token is no principal at all: 401. (Were
	// it ever resolved, requireInstanceAdmin would refuse a token principal
	// with 403 — a change this test would flag.)
	t.Run("a bearer token of an instance admin", func(t *testing.T) {
		for _, p := range reads {
			if resp := bearerRequestJSON(t, s, http.MethodGet, p, tok, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET %s: status %d, want 401", p, resp.StatusCode)
			}
		}
		for _, p := range actions {
			if resp := bearerRequestJSON(t, s, http.MethodPost, p, tok, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("POST %s: status %d, want 401", p, resp.StatusCode)
			}
		}
	})

	t.Run("a stale factor", func(t *testing.T) {
		c := s.LoginAs(admin)
		stale(t, s, admin)
		for _, p := range actions {
			resp := postJSON(t, c, s.HTTP.URL+p, nil)
			if d := problemDetail(t, resp); resp.StatusCode != http.StatusForbidden || d != "step_up_required" {
				t.Errorf("POST %s: status %d detail %q, want 403 step_up_required", p, resp.StatusCode, d)
			}
		}
		for _, p := range reads {
			if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+p, nil); resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s with a stale factor: status %d, want 200", p, resp.StatusCode)
			}
		}
	})

	// Nothing above reached the target.
	var disabled, mustChange bool
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT disabled_at IS NOT NULL, must_change_password FROM users WHERE id = $1`, target).Scan(&disabled, &mustChange); err != nil {
		t.Fatal(err)
	}
	if disabled || mustChange || len(s.Mail.Sent()) != 0 {
		t.Fatalf("a refused call changed the target: disabled %v, must change %v, mails %d", disabled, mustChange, len(s.Mail.Sent()))
	}
}

// TestAdminActionsRefuseOwnAccountAndInvitePending: no action works on the
// admin's own account; an account whose invitation is still pending allows
// only deactivation; an unknown or malformed id is refused.
func TestAdminActionsRefuseOwnAccountAndInvitePending(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "edge-admin@example.test")
	pending := seedInvitePending(t, s, "edge-pending@example.test")
	actions := []string{"temporary-password", "second-factor/reset", "sessions/revoke", "reactivate", "password-reset-link", "deactivate"}

	for _, a := range actions {
		resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(admin, a), nil)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "not_on_own_account" {
			t.Errorf("%s on one's own account: status %d detail %q, want 409 not_on_own_account", a, resp.StatusCode, d)
		}
		resp = postJSON(t, adminClient, s.HTTP.URL+adminUserPath(uuid.Must(uuid.NewV7()), a), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s on an unknown account: status %d, want 404", a, resp.StatusCode)
		}
		resp = postJSON(t, adminClient, s.HTTP.URL+adminUsersPath+"/not-a-uuid/"+a, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with a malformed id: status %d, want 400", a, resp.StatusCode)
		}
	}
	if resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the admin's session after the refusals: status %d, want 200", resp.StatusCode)
	}

	// Reactivation is allowed there: TestAdminReactivateLeavesTheInvitationPending.
	for _, a := range []string{"temporary-password", "second-factor/reset", "sessions/revoke", "password-reset-link"} {
		resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(pending, a), nil)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "invite_pending" {
			t.Errorf("%s on an invite-pending account: status %d detail %q, want 409 invite_pending", a, resp.StatusCode, d)
		}
	}
	var hasPassword bool
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT password_hash IS NOT NULL FROM users WHERE id = $1`, pending).Scan(&hasPassword); err != nil {
		t.Fatal(err)
	}
	if hasPassword {
		t.Fatal("an invite-pending account was given a password")
	}
	// Deactivating it works, and tells nobody: the account was never in
	// use.
	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(pending, "deactivate"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivating an invite-pending account: status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var body noticeBody
	decodeInto(t, resp, &body)
	if body.Notice != "skipped" || len(s.Mail.Sent()) != 0 {
		t.Fatalf("deactivating an invite-pending account: notice %q, mails %+v; want skipped and none", body.Notice, s.Mail.Sent())
	}
}

// TestDeactivateRefusesSelfAndKeepsLastAdminRule: the existing route now
// refuses the caller's own account first — the only admin included, whose
// deactivation used to be refused by the last-admin rule — and still
// deactivates a fellow admin. The last-admin rule itself stays in the
// store's atomic guard (tenancy's store test races two admins against each
// other); with the self refusal in front of it, an admin's request can only
// meet it in such a race, and the route still answers it with 409
// (TestMapTenancyErrLastInstanceAdmin).
func TestDeactivateRefusesSelfAndKeepsLastAdminRule(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "self-admin@example.test")

	resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(admin, "deactivate"), nil)
	if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "not_on_own_account" {
		t.Fatalf("the only admin deactivating themselves: status %d detail %q, want 409 not_on_own_account", resp.StatusCode, d)
	}

	second := s.SeedUser("self-second@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, second)
	secondClient := s.LoginAs(second)
	resp = postJSON(t, secondClient, s.HTTP.URL+adminUserPath(second, "deactivate"), nil)
	if d := problemDetail(t, resp); resp.StatusCode != http.StatusConflict || d != "not_on_own_account" {
		t.Fatalf("one of two admins deactivating themselves: status %d detail %q, want 409 not_on_own_account", resp.StatusCode, d)
	}

	if resp := postJSON(t, adminClient, s.HTTP.URL+adminUserPath(second, "deactivate"), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivating a fellow admin: status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	var disabled bool
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT disabled_at IS NOT NULL FROM users WHERE id = $1`, admin).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled {
		t.Fatal("a refused self-deactivation disabled the admin")
	}
	if resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the remaining admin's session: status %d, want 200", resp.StatusCode)
	}
}

// TestAdminListUsersSearchAndPaging: the list pages by e-mail and the
// search matches the display name as well as the address.
func TestAdminListUsersSearchAndPaging(t *testing.T) {
	s := apitest.New(t)
	_, adminClient := adminWithFactor(t, s, "a-admin@example.test")
	b := s.SeedUser("b-user@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET display_name = 'Robert Builder' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	seedInvitePending(t, s, "c-pending@example.test")

	list := func(query url.Values) adminUserListBody {
		t.Helper()
		resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+adminUsersPath+"?"+query.Encode(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list %s: status %d (%s)", query.Encode(), resp.StatusCode, mustReadBody(t, resp))
		}
		var out adminUserListBody
		decodeInto(t, resp, &out)
		return out
	}

	first := list(url.Values{"limit": {"2"}})
	if len(first.Users) != 2 || first.Users[0].Email != "a-admin@example.test" || first.Users[1].Email != "b-user@example.test" || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	if u := first.Users[0]; !u.InstanceAdmin || u.State != "active" || u.ID == "" {
		t.Fatalf("admin row = %+v, want an active instance admin", u)
	}
	if u := first.Users[1]; u.InstanceAdmin || u.State != "no_second_factor" || u.DisplayName != "Robert Builder" {
		t.Fatalf("user row = %+v, want a user without a second factor", u)
	}
	second := list(url.Values{"limit": {"2"}, "cursor": {first.NextCursor}})
	if len(second.Users) != 1 || second.Users[0].Email != "c-pending@example.test" || second.Users[0].State != "invite_pending" || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}

	found := list(url.Values{"q": {"builder"}})
	if len(found.Users) != 1 || found.Users[0].ID != b.String() {
		t.Fatalf("search by display name = %+v, want only %s", found.Users, b)
	}

	if resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+adminUsersPath+"?cursor=not-a-cursor!", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage cursor: status %d, want 400", resp.StatusCode)
	}
}

// TestAdminGetUser: the account view shows memberships (and whether the
// admin can open each organization), factors and active sessions.
func TestAdminGetUser(t *testing.T) {
	s := apitest.New(t)
	admin, adminClient := adminWithFactor(t, s, "view-admin@example.test")
	target := s.SeedUser("view-target@example.test", "correct horse battery")
	s.SeedPasskey(target, "key")
	shared := s.SeedOrg("view-shared", target)
	s.AddMember(shared, admin, "viewer")
	other := s.SeedUser("view-other@example.test", "correct horse battery")
	foreign := s.SeedOrg("view-foreign", other)
	s.AddMember(foreign, target, "operator")
	s.LoginAs(target)

	resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+adminUserPath(target, ""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get user: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var body adminUserDetailBody
	decodeInto(t, resp, &body)
	u := body.User
	if u.ID != target.String() || u.Email != "view-target@example.test" || u.State != "active" || u.OrgCount != 2 ||
		u.Factors.Passkeys != 1 || u.Factors.TOTP || u.ActiveSessions != 1 || u.CreatedAt.IsZero() || u.LastSignInAt == nil {
		t.Fatalf("user = %+v", u)
	}
	members := map[string]bool{}
	for _, m := range u.Memberships {
		members[m.Slug+"/"+m.Role] = m.ActorIsMember
	}
	if len(members) != 2 || !members["view-shared/owner"] || members["view-foreign/operator"] {
		t.Fatalf("memberships = %+v, want the shared org (admin is a member) and the foreign one (not)", u.Memberships)
	}

	if resp := requestJSON(t, adminClient, http.MethodGet, s.HTTP.URL+adminUserPath(uuid.Must(uuid.NewV7()), ""), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown user: status %d, want 404", resp.StatusCode)
	}
}
