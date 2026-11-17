package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type patchedToken struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Areas     []string `json:"areas"`
	ExpiresAt *string  `json:"expires_at"`
}

// mkToken creates a token through the API as client c and returns its id.
func mkToken(t *testing.T, s *apitest.Server, c *http.Client, slug string, body map[string]any) string {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/"+slug+"/tokens", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func patchToken(t *testing.T, s *apitest.Server, c *http.Client, slug, id string, body any) *http.Response {
	t.Helper()
	return requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/"+slug+"/tokens/"+id, body)
}

func listTokens(t *testing.T, s *apitest.Server, c *http.Client, slug string) []patchedToken {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+slug+"/tokens", nil)
	var listed struct {
		Tokens []patchedToken `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	return listed.Tokens
}

func sameInstant(t *testing.T, a, b string) bool {
	t.Helper()
	ta, err1 := time.Parse(time.RFC3339Nano, a)
	tb, err2 := time.Parse(time.RFC3339Nano, b)
	if err1 != nil || err2 != nil {
		t.Fatalf("parse %q / %q: %v %v", a, b, err1, err2)
	}
	return ta.Equal(tb)
}

func TestPatchTokenOwnerChangesAreas(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-areas@example.test", "correct horse battery")
	s.SeedOrg("patch-areas-org", owner)
	c := s.LoginAs(owner)
	id := mkToken(t, s, c, "patch-areas-org", map[string]any{"name": "asst", "role": "operator", "kind": "assistant", "areas": []string{"inventory.read"}})

	resp := patchToken(t, s, c, "patch-areas-org", id, map[string]any{"areas": []string{"inventory.write", "repos.read"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Token patchedToken `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	want := "inventory.write,repos.read"
	if got := strings.Join(out.Token.Areas, ","); got != want {
		t.Fatalf("response areas = %q, want %q", got, want)
	}
	listed := listTokens(t, s, c, "patch-areas-org")
	if len(listed) != 1 || strings.Join(listed[0].Areas, ",") != want {
		t.Fatalf("list = %+v", listed)
	}
}

func TestPatchTokenAPITokenNameAndExpiry(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-api@example.test", "correct horse battery")
	s.SeedOrg("patch-api-org", owner)
	c := s.LoginAs(owner)
	id := mkToken(t, s, c, "patch-api-org", map[string]any{"name": "ci", "role": "viewer"})

	resp := patchToken(t, s, c, "patch-api-org", id, map[string]any{"name": "ci-2", "expires_at": "2030-01-01T00:00:00Z"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	listed := listTokens(t, s, c, "patch-api-org")
	if len(listed) != 1 || listed[0].Name != "ci-2" || listed[0].ExpiresAt == nil || !sameInstant(t, *listed[0].ExpiresAt, "2030-01-01T00:00:00Z") {
		t.Fatalf("list = %+v (expiry %v)", listed, listed[0].ExpiresAt)
	}
	resp = patchToken(t, s, c, "patch-api-org", id, map[string]any{"never_expires": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear expiry: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if listed = listTokens(t, s, c, "patch-api-org"); listed[0].ExpiresAt != nil {
		t.Fatalf("expiry not cleared: %+v", listed)
	}
}

// An admin may edit a token an owner made; the areas are checked against the
// lower of the admin's role and the token's.
func TestPatchTokenAdminEditsOwnersToken(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-adm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("patch-adm-org", owner)
	admin := s.SeedUser("patch-adm-admin@example.test", "correct horse battery")
	s.AddMember(org, admin, tenancy.RoleAdmin)
	id := mkToken(t, s, s.LoginAs(owner), "patch-adm-org", map[string]any{"name": "asst", "role": "owner", "kind": "assistant", "areas": []string{"inventory.read"}})

	resp := patchToken(t, s, s.LoginAs(admin), "patch-adm-org", id, map[string]any{"name": "renamed", "areas": []string{"inventory.write"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin patch: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	listed := listTokens(t, s, s.LoginAs(owner), "patch-adm-org")
	if len(listed) != 1 || listed[0].Name != "renamed" || strings.Join(listed[0].Areas, ",") != "inventory.write" {
		t.Fatalf("list = %+v", listed)
	}
}

func TestPatchTokenOperatorForbidden(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-op-owner@example.test", "correct horse battery")
	org := s.SeedOrg("patch-op-org", owner)
	op := s.SeedUser("patch-op-op@example.test", "correct horse battery")
	s.AddMember(org, op, tenancy.RoleOperator)
	id := mkToken(t, s, s.LoginAs(owner), "patch-op-org", map[string]any{"name": "ci", "role": "viewer"})
	if resp := patchToken(t, s, s.LoginAs(op), "patch-op-org", id, map[string]any{"name": "x"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator patch: %d, want 403", resp.StatusCode)
	}
}

func TestPatchTokenBearerForbidden(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithTokenPerms(t, tenancy.PermTokensManage)
	path := "/api/v1/orgs/" + org.String() + "/tokens/" + uuid.NewString()
	if resp := bearerRequestJSON(t, s, http.MethodPatch, path, tok, map[string]any{"name": "x"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("api token patch: %d, want 403", resp.StatusCode)
	}
	aorg, atok := s.OrgWithAssistantToken(t, tenancy.RoleOwner, tenancy.AreaInventoryRead)
	apath := "/api/v1/orgs/" + aorg.String() + "/tokens/" + uuid.NewString()
	if resp := s.AssistantDo(t, http.MethodPatch, atok, apath, "", map[string]any{"name": "x"}); resp.Code != http.StatusForbidden {
		t.Fatalf("assistant token patch: %d %s, want 403", resp.Code, resp.Body)
	}
}

func TestPatchTokenOtherOrg404(t *testing.T) {
	s := apitest.New(t)
	a := s.SeedUser("patch-oo-a@example.test", "correct horse battery")
	s.SeedOrg("patch-oo-a", a)
	b := s.SeedUser("patch-oo-b@example.test", "correct horse battery")
	s.SeedOrg("patch-oo-b", b)
	id := mkToken(t, s, s.LoginAs(b), "patch-oo-b", map[string]any{"name": "ci", "role": "viewer"})
	if resp := patchToken(t, s, s.LoginAs(a), "patch-oo-a", id, map[string]any{"name": "x"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other org's token: %d, want 404", resp.StatusCode)
	}
}

func TestPatchTokenRevoked404(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-rev@example.test", "correct horse battery")
	s.SeedOrg("patch-rev-org", owner)
	c := s.LoginAs(owner)
	id := mkToken(t, s, c, "patch-rev-org", map[string]any{"name": "ci", "role": "viewer"})
	if resp := requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/orgs/patch-rev-org/tokens/"+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp := patchToken(t, s, c, "patch-rev-org", id, map[string]any{"name": "x"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked token: %d, want 404", resp.StatusCode)
	}
}

func TestPatchTokenExpired409(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-exp@example.test", "correct horse battery")
	s.SeedOrg("patch-exp-org", owner)
	c := s.LoginAs(owner)
	id := mkToken(t, s, c, "patch-exp-org", map[string]any{"name": "ci", "role": "viewer"})
	if _, err := s.DB.Pool().Exec(context.Background(), `UPDATE api_tokens SET expires_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	resp := patchToken(t, s, c, "patch-exp-org", id, map[string]any{"name": "x"})
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "expired") {
		t.Fatalf("expired token: %d %s, want 409", resp.StatusCode, body)
	}
}

func TestPatchTokenValidation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-val@example.test", "correct horse battery")
	s.SeedOrg("patch-val-org", owner)
	c := s.LoginAs(owner)
	apiID := mkToken(t, s, c, "patch-val-org", map[string]any{"name": "ci", "role": "viewer"})
	asstID := mkToken(t, s, c, "patch-val-org", map[string]any{"name": "asst", "role": "operator", "kind": "assistant", "areas": []string{"inventory.read"}})
	viewerID := mkToken(t, s, c, "patch-val-org", map[string]any{"name": "vasst", "role": "viewer", "kind": "assistant", "areas": []string{"inventory.read"}})

	cases := []struct {
		name string
		id   string
		body map[string]any
		msg  string
	}{
		{"empty body", apiID, map[string]any{}, "nothing to change"},
		{"both expiry fields", apiID, map[string]any{"expires_at": "2030-01-01T00:00:00Z", "never_expires": true}, "exclude each other"},
		{"expiry in the past", apiID, map[string]any{"expires_at": "2020-01-01T00:00:00Z"}, "in the future"},
		{"areas on an api token", apiID, map[string]any{"areas": []string{"inventory.read"}}, "only assistant tokens have areas"},
		{"empty areas", asstID, map[string]any{"areas": []string{}}, "at least one area"},
		{"unknown area", asstID, map[string]any{"areas": []string{"everything"}}, "unknown area"},
		{"duplicate area", asstID, map[string]any{"areas": []string{"inventory.read", "inventory.read"}}, "listed twice"},
		{"area above the role", viewerID, map[string]any{"areas": []string{"inventory.write"}}, "inventory.write"},
	}
	for _, tc := range cases {
		resp := patchToken(t, s, c, "patch-val-org", tc.id, tc.body)
		body := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, tc.msg) {
			t.Errorf("%s: %d %s, want 422 containing %q", tc.name, resp.StatusCode, body, tc.msg)
		}
	}
	if resp := patchToken(t, s, c, "patch-val-org", "not-a-uuid", map[string]any{"name": "x"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id: %d, want 400", resp.StatusCode)
	}
}

func TestPatchTokenAuditSummary(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-aud@example.test", "correct horse battery")
	s.SeedOrg("patch-aud-org", owner)
	c := s.LoginAs(owner)
	asst := mkToken(t, s, c, "patch-aud-org", map[string]any{"name": "t", "role": "operator", "kind": "assistant", "areas": []string{"inventory.read"}})
	asst2 := mkToken(t, s, c, "patch-aud-org", map[string]any{"name": "t", "role": "operator", "kind": "assistant", "areas": []string{"inventory.read"}})
	api := mkToken(t, s, c, "patch-aud-org", map[string]any{"name": "ci", "role": "viewer"})

	for _, step := range []struct {
		id   string
		body map[string]any
	}{
		{asst, map[string]any{"areas": []string{"inventory.write", "repos.read"}, "expires_at": "2030-01-01T00:00:00Z"}},
		{asst2, map[string]any{"areas": []string{"inventory.write", "repos.read"}, "expires_at": "2030-01-01T02:00:00+02:00"}},
		{api, map[string]any{"name": "ci-2"}},
		{api, map[string]any{"name": "ci-2"}},
	} {
		if resp := patchToken(t, s, c, "patch-aud-org", step.id, step.body); resp.StatusCode != http.StatusOK {
			t.Fatalf("patch %v: %d %s", step.body, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	got := map[string]int{}
	activity := decodeActivity(t, requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/patch-aud-org/activity?limit=50", nil))
	for _, e := range activity.Items {
		if e.Action == "PATCH updateToken" {
			got[e.Summary]++
		}
	}
	want := map[string]int{
		"changed assistant token t: areas inventory.read → inventory.write, repos.read; expiry never → 2030-01-01T00:00:00Z": 2,
		"changed API token ci: name ci → ci-2": 1,
		"changed API token ci-2: no change":    1,
	}
	// "no change" is reachable: an unchanged name is still a change request.
	for k, n := range want {
		if got[k] != n {
			t.Errorf("summary %q seen %d times, want %d; all: %v", k, got[k], n, got)
		}
	}
}

func TestPatchTokenTakesEffectImmediately(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("patch-fx@example.test", "correct horse battery")
	org := s.SeedOrg("patch-fx-org", owner)
	c := s.LoginAs(owner)
	tok := s.AssistantTokenFor(t, org, owner, tenancy.RoleOwner, tenancy.AreaInventoryRead)
	var id string
	for _, tk := range listTokens(t, s, c, "patch-fx-org") {
		id = tk.ID
	}
	repos := "/api/v1/orgs/patch-fx-org/repos"
	if resp := s.AssistantDo(t, http.MethodGet, tok, repos, "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("before: %d, want 403", resp.Code)
	}
	if resp := patchToken(t, s, c, "patch-fx-org", id, map[string]any{"areas": []string{"inventory.read", "repos.read"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := s.AssistantDo(t, http.MethodGet, tok, repos, "", nil); resp.Code != http.StatusOK {
		t.Fatalf("after adding repos.read: %d %s, want 200", resp.Code, resp.Body)
	}
	if resp := patchToken(t, s, c, "patch-fx-org", id, map[string]any{"areas": []string{"inventory.read"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch back: %d", resp.StatusCode)
	}
	if resp := s.AssistantDo(t, http.MethodGet, tok, repos, "", nil); resp.Code != http.StatusForbidden {
		t.Fatalf("after removing: %d, want 403", resp.Code)
	}
}
