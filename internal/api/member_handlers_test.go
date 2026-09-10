package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
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
// session cookie stops working immediately after deactivation.
func TestDeactivateUserIsInstanceAdminOnly(t *testing.T) {
	s := apitest.New(t)
	target := s.SeedUser("deactivate-target@example.test", "correct horse battery")
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
}
