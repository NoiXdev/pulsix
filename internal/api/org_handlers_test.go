package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// requestJSON issues method against url with an optional JSON body (nil for
// none), using c — the general-purpose request helper org_handlers_test.go,
// member_handlers_test.go and token_routes_test.go all share, alongside
// postJSON (auth_handlers_test.go) for the POST-only case.
func requestJSON(t *testing.T, c *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type orgDTO struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

func decodeOrg(t *testing.T, resp *http.Response) orgDTO {
	t.Helper()
	var out struct {
		Org orgDTO `json:"org"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Org
}

func TestCreateOrgAndListMine(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("org-owner@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs", map[string]any{"slug": "acme", "name": "Acme"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create org: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	created := decodeOrg(t, resp)
	if created.Slug != "acme" || created.Name != "Acme" || created.Role != "owner" || created.ID == "" {
		t.Fatalf("created org = %+v", created)
	}

	resp = requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list orgs: status %d", resp.StatusCode)
	}
	var listed struct {
		Orgs []orgDTO `json:"orgs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Orgs) != 1 || listed.Orgs[0].Slug != "acme" || listed.Orgs[0].Role != "owner" {
		t.Fatalf("listed orgs = %+v", listed.Orgs)
	}
}

func TestCreateOrgDuplicateSlugConflict(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("dup-owner@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs", map[string]any{"slug": "dup", "name": "Dup"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status %d", resp.StatusCode)
	}
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs", map[string]any{"slug": "dup", "name": "Dup Again"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate slug: status %d, want 409", resp.StatusCode)
	}
}

func TestCreateOrgRequiresLogin(t *testing.T) {
	s := apitest.New(t)
	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/orgs", map[string]any{"slug": "acme", "name": "Acme"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("create with no principal: status %d, want 401", resp.StatusCode)
	}
}

// TestGetOrgRequiresMembership proves a caller who is not a member of the
// org gets 403, not (say) an accidental 404 or a leak of the org's data.
func TestGetOrgRequiresMembership(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("org-a-owner@example.test", "correct horse battery")
	s.SeedOrg("org-a", owner)
	outsider := s.SeedUser("outsider@example.test", "correct horse battery")
	c := s.LoginAs(outsider)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/org-a", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member get: status %d, want 403, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestGetOrgUnknownSlugNotFound(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("org-lookup@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown org: status %d, want 404", resp.StatusCode)
	}
}

// TestOrgScopedRouteRequiresMFAComplete proves requireOrg's second step: a
// session that has not completed a required second factor is rejected with
// 403 "second factor required" before org membership is even looked up.
func TestOrgScopedRouteRequiresMFAComplete(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("mfa-pending@example.test", "correct horse battery")
	s.SeedOrg("mfa-pending-org", uid)
	c := s.LoginAsPending(uid)

	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/mfa-pending-org", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pending session: status %d, want 403", resp.StatusCode)
	}
	var body struct {
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Detail != "second factor required" {
		t.Fatalf("detail = %q, want %q", body.Detail, "second factor required")
	}
}

func TestPatchOrgRequiresOrgManage(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-owner@example.test", "correct horse battery")
	org := s.SeedOrg("patch-org", owner)
	viewer := s.SeedUser("patch-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)

	viewerClient := s.LoginAs(viewer)
	resp := requestJSON(t, viewerClient, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/patch-org", map[string]any{"name": "New Name"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer patch: status %d, want 403", resp.StatusCode)
	}

	ownerClient := s.LoginAs(owner)
	resp = requestJSON(t, ownerClient, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/patch-org", map[string]any{"name": "New Name"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner patch: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	updated := decodeOrg(t, resp)
	if updated.Name != "New Name" || updated.Slug != "patch-org" {
		t.Fatalf("updated org = %+v", updated)
	}
}

func mustReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
