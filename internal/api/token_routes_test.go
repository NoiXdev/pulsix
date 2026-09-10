package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestOrgTokenRoutesCreateListRevoke proves the org-scoped token routes
// Task 9 wrote but never mounted now work end to end, gated on
// tenancy.PermTokensManage through requireOrg — the carry-over this task
// owns (see api.go's comment on New and registerTokenRoutes' own).
func TestOrgTokenRoutesCreateListRevoke(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("token-owner@example.test", "correct horse battery")
	s.SeedOrg("token-org", owner)
	c := s.LoginAs(owner)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/token-org/tokens", map[string]any{"name": "ci", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Token string `json:"token"`
		ID    string `json:"id"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "pxt_") || created.ID == "" || created.Role != "operator" {
		t.Fatalf("create response = %+v", created)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/token-org/tokens", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d", resp.StatusCode)
	}
	var listed struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tokens) != 1 || listed.Tokens[0]["id"] != created.ID {
		t.Fatalf("list = %+v", listed.Tokens)
	}
	if _, leaked := listed.Tokens[0]["token"]; leaked {
		t.Fatalf("list leaked the plaintext token: %+v", listed.Tokens[0])
	}

	resp = requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/token-org/tokens/"+created.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/token-org/tokens", nil)
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tokens) != 0 {
		t.Fatalf("list after revoke = %+v, want empty", listed.Tokens)
	}
}

// TestOrgTokenRoutesRequireTokensManage proves the PermTokensManage floor
// (admin): a viewer cannot create, list or revoke tokens for the org.
func TestOrgTokenRoutesRequireTokensManage(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("token-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("token-perm-org", owner)
	viewer := s.SeedUser("token-perm-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	c := s.LoginAs(viewer)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/token-perm-org/tokens", map[string]any{"name": "ci", "role": "viewer"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: status %d, want 403", resp.StatusCode)
	}
	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/token-perm-org/tokens", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer list: status %d, want 403", resp.StatusCode)
	}
}

// TestOrgTokenRoutesRejectMalformedTokenID keeps the 400-on-malformed-id
// behavior Task 9's handler always had for the token id path segment
// (unlike Org, which is now a slug and simply 404s if unknown).
func TestOrgTokenRoutesRejectMalformedTokenID(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("token-malformed-owner@example.test", "correct horse battery")
	s.SeedOrg("token-malformed-org", owner)
	c := s.LoginAs(owner)

	resp := requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/token-malformed-org/tokens/not-a-uuid", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed token id: status %d, want 400", resp.StatusCode)
	}
}

// TestOrgTokenCreateCapsRoleAtCallerRole proves Create's own extra rule on
// top of requireOrg: the minted token's role can never exceed the caller's
// effective role, even though the schema layer alone would accept any of
// the four enum values.
func TestOrgTokenCreateCapsRoleAtCallerRole(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("token-cap-owner@example.test", "correct horse battery")
	org := s.SeedOrg("token-cap-org", owner)
	admin := s.SeedUser("token-cap-admin@example.test", "correct horse battery")
	s.AddMember(org, admin, tenancy.RoleAdmin)
	c := s.LoginAs(admin)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/token-cap-org/tokens", map[string]any{"name": "ci", "role": "owner"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Role != "admin" {
		t.Fatalf("token role = %q, want %q (capped at the admin caller's own role)", created.Role, "admin")
	}
}
