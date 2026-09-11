package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type memberDTO struct {
	UserID      string  `json:"user_id"`
	Email       string  `json:"email"`
	DisplayName string  `json:"display_name"`
	Role        string  `json:"role"`
	DisabledAt  *string `json:"disabled_at"`
	CreatedAt   string  `json:"created_at"`
}

// TestInviteAcceptAppearsInMembers is the invite/accept/list happy path:
// owner invites a new address, the (fake) mailer captures the message
// carrying the token, the invitee accepts it and shows up in the member
// list at the invited role.
func TestInviteAcceptAppearsInMembers(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("invite-owner@example.test", "correct horse battery")
	s.SeedOrg("invite-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/invite-org/invites", map[string]any{"email": "invitee@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	sent := s.Mail.Sent()
	if len(sent) != 1 || sent[0].To != "invitee@example.test" {
		t.Fatalf("mail sent = %+v", sent)
	}
	const marker = "/invite/"
	i := strings.Index(sent[0].Body, marker)
	if i < 0 {
		t.Fatalf("invite mail has no token link: %q", sent[0].Body)
	}
	token := strings.TrimSpace(sent[0].Body[i+len(marker):])
	if nl := strings.IndexAny(token, "\n\r"); nl >= 0 {
		token = token[:nl]
	}
	if token == "" {
		t.Fatalf("empty token extracted from mail body: %q", sent[0].Body)
	}

	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Invitee", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var accepted struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"org_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.UserID == "" || accepted.OrgID == "" {
		t.Fatalf("accept response = %+v", accepted)
	}

	// Accepting the same token again must fail: single-use.
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Invitee", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("reused invite token: status %d, want 404", resp.StatusCode)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/invite-org/members", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list members: status %d", resp.StatusCode)
	}
	var listed struct {
		Members []memberDTO `json:"members"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	var found *memberDTO
	for i := range listed.Members {
		if listed.Members[i].UserID == accepted.UserID {
			found = &listed.Members[i]
		}
	}
	if found == nil {
		t.Fatalf("invitee not in member list: %+v", listed.Members)
	}
	if found.Email != "invitee@example.test" || found.Role != "operator" {
		t.Fatalf("invited member = %+v", found)
	}
}

// TestInviteAcceptNewAccountOpensEnrolmentSession is the fix for the
// product bug task-15b-report.md's "Product bug #2" reported but left
// unfixed: accepting an invite that creates a brand-new account (this
// address never had a password before) must open a session right away, so
// the invitee's browser continues straight to second-factor enrolment
// instead of landing on a login-required dead end. A second factor is
// mandatory, so that session is not MFA-complete: it is refused on the
// organization it just joined until it enrols one, and enrolling completes
// it with no fresh login.
func TestInviteAcceptNewAccountOpensEnrolmentSession(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("newacct-owner@example.test", "correct horse battery")
	s.SeedOrg("newacct-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/newacct-org/invites", map[string]any{"email": "brand-new@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	token := extractInviteToken(t, s)

	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Brand New", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	cookies := resp.Cookies()
	var sessionCookie *http.Cookie
	for _, ck := range cookies {
		if ck.Name == auth.CookieName {
			sessionCookie = ck
		}
	}
	if sessionCookie == nil {
		t.Fatalf("accept response set no %s cookie: %+v", auth.CookieName, resp.Header.Values("Set-Cookie"))
	}
	principal, err := s.Sessions.Lookup(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatalf("look up session from accept's cookie: %v", err)
	}
	if principal.MFAComplete {
		t.Fatal("session opened by accepting a new-account invite is MFA-complete before any second factor exists")
	}

	invitee := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	invitee.Jar.SetCookies(u, []*http.Cookie{sessionCookie})
	if resp := requestJSON(t, invitee, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/newacct-org/hosts", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("organization route before enrolment: status %d, want 403", resp.StatusCode)
	}
	enrolAndConfirmTOTP(t, s, invitee)
	if resp := requestJSON(t, invitee, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/newacct-org/hosts", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("organization route after enrolment, same session: status %d, want 200", resp.StatusCode)
	}
}

// TestInviteAcceptExistingAccountOpensNoSessionAndKeepsPassword is the
// other half of the same fix: an address that already belongs to an
// account with its own password must gain nothing from an intercepted
// invitation — no session, and the account's real password (not the one
// submitted at acceptance) is what still signs it in.
func TestInviteAcceptExistingAccountOpensNoSessionAndKeepsPassword(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("existacct-owner@example.test", "correct horse battery")
	s.SeedOrg("existacct-org", owner)
	s.SeedUser("already-has-account@example.test", "the-real-password")
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/existacct-org/invites", map[string]any{"email": "already-has-account@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	token := extractInviteToken(t, s)

	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Attacker-Chosen Name", "password": "attacker-chosen-password",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName {
			t.Fatalf("accept for an existing account set a session cookie: %+v", ck)
		}
	}
	var accepted struct {
		SignedIn bool `json:"signed_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.SignedIn {
		t.Fatal("accept for an existing account reported signed_in")
	}

	// The account's real password still works.
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]any{
		"email": "already-has-account@example.test", "password": "the-real-password",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login with the real password: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The password submitted at acceptance was never applied.
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]any{
		"email": "already-has-account@example.test", "password": "attacker-chosen-password",
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatal("login with the attacker-chosen password from acceptance unexpectedly succeeded")
	}
}

// extractInviteToken pulls the invite token out of the most recently sent
// mail, the same way TestInviteAcceptAppearsInMembers does inline — a real
// invitee only ever learns the token this way (through the link in the
// e-mail), never from the invite-creation response itself.
func extractInviteToken(t *testing.T, s *apitest.Server) string {
	t.Helper()
	sent := s.Mail.Sent()
	if len(sent) == 0 {
		t.Fatal("no invite mail was sent")
	}
	body := sent[len(sent)-1].Body
	const marker = "/invite/"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("invite mail has no token link: %q", body)
	}
	token := strings.TrimSpace(body[i+len(marker):])
	if nl := strings.IndexAny(token, "\n\r"); nl >= 0 {
		token = token[:nl]
	}
	if token == "" {
		t.Fatalf("empty token extracted from mail body: %q", body)
	}
	return token
}

// TestInviteRequiresMembersManage proves an operator (below the
// PermMembersManage floor, which is admin) cannot invite.
func TestInviteRequiresMembersManage(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("invite-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("invite-perm-org", owner)
	operator := s.SeedUser("invite-perm-operator@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	c := s.LoginAs(operator)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/invite-perm-org/invites", map[string]any{"email": "someone@example.test", "role": "viewer"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator invite: status %d, want 403", resp.StatusCode)
	}
}

func TestInviteRejectsOwnerRoleAtSchemaLayer(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("invite-owner-role@example.test", "correct horse battery")
	s.SeedOrg("invite-owner-role-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/invite-owner-role-org/invites", map[string]any{"email": "someone@example.test", "role": "owner"})
	if resp.StatusCode != http.StatusUnprocessableEntity && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invite owner role: status %d, want 400 or 422", resp.StatusCode)
	}
}

// TestChangeRoleAndRemoveMember exercises PATCH and DELETE on a member
// end-to-end, including the last-owner guard returning 409.
func TestChangeRoleAndRemoveMember(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("role-owner@example.test", "correct horse battery")
	org := s.SeedOrg("role-org", owner)
	member := s.SeedUser("role-member@example.test", "correct horse battery")
	s.AddMember(org, member, tenancy.RoleViewer)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/role-org/members/"+member.String(), map[string]any{"role": "admin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change role: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/role-org/members", nil)
	var listed struct {
		Members []memberDTO `json:"members"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range listed.Members {
		if m.UserID == member.String() && m.Role == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("member role not updated: %+v", listed.Members)
	}

	// The sole owner cannot demote themself: 409.
	resp = requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/role-org/members/"+owner.String(), map[string]any{"role": "admin"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("demote last owner: status %d, want 409", resp.StatusCode)
	}

	resp = requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/role-org/members/"+member.String(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove member: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/role-org/members", nil)
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Members) != 1 {
		t.Fatalf("members after remove = %+v, want just the owner", listed.Members)
	}
}

// TestBearerTokenRoleCapAppliesToOwnershipOperations is Finding 1's
// end-to-end regression test (Task 10 fix round 1): requireOrg computes
// Scope.Role as the lower of the caller's real membership role and the
// bearer token's own scoped role, but the service used to re-derive the
// actor's role from the store — silently discarding that cap — to decide
// its owner-only rules. An org owner who mints an admin-scoped token (a
// realistic CI-credential scenario) must not be able to use that token to
// promote another member to owner or to remove an existing owner, even
// though the token owner's real membership is owner and an admin-scoped
// token otherwise passes the PermMembersManage floor (admin) both routes
// gate on.
func TestBearerTokenRoleCapAppliesToOwnershipOperations(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("role-cap-owner@example.test", "correct horse battery")
	org := s.SeedOrg("role-cap-org", owner)
	secondOwner := s.SeedUser("role-cap-owner2@example.test", "correct horse battery")
	s.AddMember(org, secondOwner, tenancy.RoleOwner)
	target := s.SeedUser("role-cap-target@example.test", "correct horse battery")
	s.AddMember(org, target, tenancy.RoleAdmin)

	plain, _, err := s.Deps.Tokens.Create(context.Background(), owner, org, "ci", "admin", nil)
	if err != nil {
		t.Fatalf("Create token: %v", err)
	}

	resp := bearerRequestJSON(t, s, http.MethodPatch, "/api/v1/orgs/role-cap-org/members/"+target.String(), plain, map[string]any{"role": "owner"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin-capped token promoting to owner: status %d, want 403, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	resp = bearerRequest(t, s, http.MethodDelete, "/api/v1/orgs/role-cap-org/members/"+secondOwner.String(), plain)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin-capped token removing an owner: status %d, want 403, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestChangeRoleMalformedUserID(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("malformed-owner@example.test", "correct horse battery")
	s.SeedOrg("malformed-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/malformed-org/members/not-a-uuid", map[string]any{"role": "admin"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed user id: status %d, want 400", resp.StatusCode)
	}
}

// TestDeactivateUserIsInstanceAdminOnly proves the admin route rejects a
// non-instance-admin caller and, when it succeeds, actually ends the
// user's access rather than only flagging the row: an already-issued
// session cookie stops working immediately after deactivation, and — the
// gap Finding 2 (Task 10 fix round 1) found, which the pre-fix version of
// this test could not catch because it only ever reused the old cookie —
// a fresh login with the same, still-correct password no longer succeeds
// either. The passkey entry point is closed by the same disabled_at check
// but is exercised at the unit level instead, in
// internal/auth/passkey_clone_test.go's TestFinishLoginRejectsDisabledUser:
// driving a real WebAuthn assertion through this HTTP server needs a
// virtual authenticator, which lands with Task 15.
func TestDeactivateUserIsInstanceAdminOnly(t *testing.T) {
	s := apitest.New(t)
	const targetPassword = "correct horse battery"
	target := s.SeedUser("deactivate-target@example.test", targetPassword)
	targetClient := s.LoginAs(target)

	regular := s.SeedUser("deactivate-regular@example.test", "correct horse battery")
	resp := postJSON(t, s.LoginAs(regular), s.HTTP.URL+"/api/v1/admin/users/"+target.String()+"/deactivate", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin deactivate: status %d, want 403", resp.StatusCode)
	}

	admin := s.SeedUser("deactivate-admin@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), "UPDATE users SET instance_admin = true WHERE id = $1", admin); err != nil {
		t.Fatal(err)
	}
	resp = postJSON(t, s.LoginAs(admin), s.HTTP.URL+"/api/v1/admin/users/"+target.String()+"/deactivate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin deactivate: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The target's already-issued session must stop working, not merely a
	// disabled_at flag with no immediate effect.
	resp = requestJSON(t, targetClient, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deactivated user's session: status %d, want 401 (session must be revoked, not just flagged)", resp.StatusCode)
	}

	// A fresh login with the same, still-correct password must also fail —
	// otherwise deactivation would only end the current session and the
	// user could immediately reauthenticate.
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/login", map[string]any{
		"email": "deactivate-target@example.test", "password": targetPassword,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("fresh login for a deactivated user: status %d, want 401", resp.StatusCode)
	}
}

// TestDeactivateRefusesTheLastInstanceAdmin covers M6 over HTTP: the only
// active instance administrator cannot deactivate themselves (setup is gated
// on "no instance administrator exists", so the instance would be
// unrecoverable), and an unknown user id is a 404 rather than a silent 200.
func TestDeactivateRefusesTheLastInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("only-admin@example.test", "correct horse battery")
	if _, err := s.DB.Pool().Exec(t.Context(), "UPDATE users SET instance_admin = true WHERE id = $1", admin); err != nil {
		t.Fatal(err)
	}
	c := s.LoginAs(admin)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/admin/users/"+admin.String()+"/deactivate", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deactivating the last instance admin: status %d, want 409; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the refused deactivation still ended the admin's session: status %d", resp.StatusCode)
	}

	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/admin/users/"+uuid.Must(uuid.NewV7()).String()+"/deactivate", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deactivating an unknown user: status %d, want 404", resp.StatusCode)
	}
}
