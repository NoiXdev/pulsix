package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type pendingInviteDTO struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	InvitedByName string `json:"invited_by_name"`
	ExpiresAt     string `json:"expires_at"`
}

func listInvites(t *testing.T, c *http.Client, url string) []pendingInviteDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list invites: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Invites []pendingInviteDTO `json:"invites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Invites
}

// TestPendingInvitesCanBeListedAndRevoked is backlog 7.29's revocation
// half: an admin sees the organization's open invitations and can withdraw
// one, after which its link no longer works.
func TestPendingInvitesCanBeListedAndRevoked(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("revoke-owner@example.test", "correct horse battery")
	s.SeedOrg("revoke-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/revoke-org/invites"

	if resp := postJSON(t, c, base, map[string]any{"email": "wrong-address@example.test", "role": "admin"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d", resp.StatusCode)
	}
	token := extractInviteToken(t, s)

	invites := listInvites(t, c, base)
	if len(invites) != 1 || invites[0].Email != "wrong-address@example.test" || invites[0].Role != "admin" || invites[0].InvitedByName == "" {
		t.Fatalf("pending invites = %+v", invites)
	}

	if resp := requestJSON(t, c, http.MethodDelete, base+"/"+invites[0].ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := listInvites(t, c, base); len(got) != 0 {
		t.Fatalf("pending invites after revoking = %+v", got)
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/"+invites[0].ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoking it again: status %d, want 404", resp.StatusCode)
	}
	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Too Late", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("accepting a revoked invite: status %d, want 404", resp.StatusCode)
	}
}

// TestARemovedAdminsInviteNoLongerWorks is backlog 7.29's main finding over
// HTTP: an admin invites an address of their own, is removed, and the
// invitation must not bring them back.
func TestARemovedAdminsInviteNoLongerWorks(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("removal-owner@example.test", "correct horse battery")
	org := s.SeedOrg("removal-org", owner)
	admin := s.SeedUser("removal-admin@example.test", "correct horse battery")
	s.AddMember(org, admin, tenancy.RoleAdmin)

	ac := s.LoginAs(admin)
	if resp := postJSON(t, ac, s.HTTP.URL+"/api/v1/orgs/removal-org/invites", map[string]any{"email": "admins-other-address@example.test", "role": "admin"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d", resp.StatusCode)
	}
	token := extractInviteToken(t, s)

	oc := s.LoginAs(owner)
	if resp := requestJSON(t, oc, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/removal-org/members/"+admin.String(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("remove admin: status %d", resp.StatusCode)
	}
	if got := listInvites(t, oc, s.HTTP.URL+"/api/v1/orgs/removal-org/invites"); len(got) != 0 {
		t.Fatalf("the removed admin's invitation is still listed as open: %+v", got)
	}
	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Back Again", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("accepting the removed admin's invite: status %d, want 404", resp.StatusCode)
	}
}

// TestListAndRevokeInvitesRequireMembersManage: the list shows who is
// about to join and the revoke decides it, so both sit at the same bar as
// inviting.
func TestListAndRevokeInvitesRequireMembersManage(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("revoke-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("revoke-perm-org", owner)
	operator := s.SeedUser("revoke-perm-operator@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	c := s.LoginAs(operator)

	base := s.HTTP.URL + "/api/v1/orgs/revoke-perm-org/invites"
	if resp := requestJSON(t, c, http.MethodGet, base, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator list: status %d, want 403", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/01900000-0000-7000-8000-000000000001", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator revoke: status %d, want 403", resp.StatusCode)
	}
}
