package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// passwordChangeRequired is the refusal detail the SPA matches on.
const passwordChangeRequired = "password_change_required"

// markMustChange raises the account's forced-change flag the way an
// administrator's temporary password does.
func markMustChange(t *testing.T, s *apitest.Server, uid uuid.UUID) {
	t.Helper()
	if _, err := s.DB.Pool().Exec(t.Context(),
		`UPDATE users SET must_change_password = true, temp_password_expires_at = now() + interval '24 hours' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
}

// problemDetail reads a huma error body's detail.
func problemDetail(t *testing.T, resp *http.Response) string {
	t.Helper()
	var problem struct {
		Detail string `json:"detail"`
	}
	body := mustReadBody(t, resp)
	_ = json.Unmarshal([]byte(body), &problem)
	return problem.Detail
}

// TestMustChangePasswordPasskeySession: a session created already complete
// — the passkey sign-in path, which never sees the password — is gated all
// the same, because the flag is on the account.
func TestMustChangePasswordPasskeySession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("passkey-must-change@example.test", "correct horse battery")
	s.SeedPasskey(uid, "key")
	markMustChange(t, s, uid)
	c := s.LoginAs(uid) // Sessions.Create(uid, true), as Passkeys.FinishLogin does

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /orgs: status %d, want 403", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != passwordChangeRequired {
		t.Fatalf("GET /orgs: detail %q, want %q", d, passwordChangeRequired)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/me: status %d, want 200", resp.StatusCode)
	}
	var me struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !me.MustChangePassword {
		t.Fatal("/auth/me must_change_password = false, want true")
	}
}

// TestMustChangePasswordPendingSessionCanEnrol: a must-change account
// whose second factor was reset signs in with the temporary password and
// must enrol first; the gate never stands in the way of the enrolment
// routes, the passkey list the enrolment page reads included. Confirming
// completes the session, and from then on the change comes first: the
// enrolment routes refuse the complete session like everything else.
func TestMustChangePasswordPendingSessionCanEnrol(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("enrol-must-change@example.test", "correct horse battery")
	markMustChange(t, s, uid)
	c := s.LoginAsPending(uid)

	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/passkeys", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/passkeys: status %d (%s), want 200", resp.StatusCode, mustReadBody(t, resp))
	}
	enrolAndConfirmTOTP(t, s, c)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	var me struct {
		MFAComplete        bool `json:"mfa_complete"`
		MustChangePassword bool `json:"must_change_password"`
	}
	decodeInto(t, resp, &me)
	if !me.MFAComplete || !me.MustChangePassword {
		t.Fatalf("/auth/me after confirming = %+v, want a complete session that must change its password", me)
	}

	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/auth/totp/enrol", map[string]any{}},
		{http.MethodPost, "/api/v1/auth/totp/confirm", map[string]any{"code": "123456"}},
		{http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil},
		{http.MethodGet, "/api/v1/auth/passkeys", nil},
	} {
		resp := requestJSON(t, c, r.method, s.HTTP.URL+r.path, r.body)
		if d := problemDetail(t, resp); resp.StatusCode != http.StatusForbidden || d != passwordChangeRequired {
			t.Errorf("%s %s after enrolling: status %d detail %q, want 403 %s", r.method, r.path, resp.StatusCode, d, passwordChangeRequired)
		}
	}
}

// TestMustChangePasswordTokenUnaffected: an API token does not sign in
// with a password, so the flag does not gate it (controller's ruling).
func TestMustChangePasswordTokenUnaffected(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	var owner uuid.UUID
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT user_id FROM org_memberships WHERE org_id = $1`, org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	markMustChange(t, s, owner)

	if resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()); resp.Code != http.StatusOK {
		t.Fatalf("token GET /orgs/{org}: status %d (%s), want 200", resp.Code, resp.Body)
	}
	// The same account's cookie session is gated.
	resp := requestJSON(t, s.LoginAs(owner), http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+org.String(), nil)
	if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != passwordChangeRequired {
		t.Fatalf("cookie GET /orgs/{org}: status %d, want 403 %s", resp.StatusCode, passwordChangeRequired)
	}
}

// TestMustChangePasswordInviteAcceptNeedsTheChange: accepting an invitation
// joins the signed-in account to an organization, so a must-change session
// does not count as signed in there either. It is told to change its
// password (403 password_change_required, which sends the SPA to the change
// page) rather than to sign in (409), which it already is (backlog 7.82).
func TestMustChangePasswordInviteAcceptNeedsTheChange(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("invite-owner@example.test", "correct horse battery")
	s.SeedOrg("invite-org", owner)
	invitee := s.SeedUser("invitee-must-change@example.test", "correct horse battery")

	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/invite-org/invites",
		map[string]any{"email": "invitee-must-change@example.test", "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invite: status %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	_ = resp.Body.Close()
	token := extractInviteToken(t, s)

	markMustChange(t, s, invitee)
	resp = postJSON(t, s.LoginAs(invitee), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{"token": token})
	if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != passwordChangeRequired {
		t.Fatalf("accept with a must-change session: status %d, want 403 %s", resp.StatusCode, passwordChangeRequired)
	}
	var n int
	if err := s.DB.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM org_memberships WHERE user_id = $1`, invitee).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the must-change account joined %d organizations", n)
	}
}
