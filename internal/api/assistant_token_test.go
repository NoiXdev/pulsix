package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestAssistantTokenRefusedByREST: an assistant token presented to the REST
// API directly is refused, whatever its role and areas (spec 2026-11-16
// assistant access, "The REST API refuses assistant tokens").
func TestAssistantTokenRefusedByREST(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithAssistantToken(t, tenancy.RoleOwner, tenancy.AreaInventoryRead)
	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/hosts")
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "assistant tokens work only at the assistant endpoint") {
		t.Fatalf("GET hosts with an assistant token = %d %s, want 403", resp.Code, resp.Body)
	}
}

// TestAssistantTokenAreasNarrowPermissions: through the endpoint's mark, an
// assistant token reaches what its areas grant and nothing else.
func TestAssistantTokenAreasNarrowPermissions(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithAssistantToken(t, tenancy.RoleOwner, tenancy.AreaInventoryRead)
	base := "/api/v1/orgs/" + org.String()
	if resp := s.AssistantDo(t, http.MethodGet, tok, base+"/hosts", "", nil); resp.Code != http.StatusOK {
		t.Fatalf("hosts with inventory.read = %d %s", resp.Code, resp.Body)
	}
	resp := s.AssistantDo(t, http.MethodGet, tok, base+"/repos", "", nil)
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "the token's areas do not include repos.read") {
		t.Fatalf("repos with inventory.read only = %d %s, want 403", resp.Code, resp.Body)
	}
	resp = s.AssistantDo(t, http.MethodPost, tok, base+"/inventory/groups", "", map[string]any{"name": "web"})
	if resp.Code != http.StatusForbidden {
		t.Fatalf("group create with inventory.read only = %d %s, want 403", resp.Code, resp.Body)
	}
}

// TestAssistantTokenRoleStillCaps: an area never widens the token's role.
func TestAssistantTokenRoleStillCaps(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithAssistantToken(t, tenancy.RoleViewer, tenancy.AreaInventoryWrite)
	resp := s.AssistantDo(t, http.MethodPost, tok, "/api/v1/orgs/"+org.String()+"/inventory/groups", "", map[string]any{"name": "web"})
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "insufficient role") {
		t.Fatalf("viewer-capped assistant token creating a group = %d %s, want 403", resp.Code, resp.Body)
	}
}

// TestAPITokenUnaffectedByTheMark: an ordinary token keeps working at REST.
func TestAPITokenUnaffectedByTheMark(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	if resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/repos"); resp.Code != http.StatusOK {
		t.Fatalf("api token at REST = %d %s", resp.Code, resp.Body)
	}
}

func TestCreateAssistantTokenValidation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("asst-owner@example.test", "correct horse battery")
	s.SeedOrg("asst-org", owner)
	c := s.LoginAs(owner)
	url := s.HTTP.URL + "/api/v1/orgs/asst-org/tokens"

	cases := []struct {
		name string
		body map[string]any
		want int
		msg  string
	}{
		{"assistant without areas", map[string]any{"name": "a", "role": "operator", "kind": "assistant"}, http.StatusUnprocessableEntity, "an assistant token needs at least one area"},
		{"api with areas", map[string]any{"name": "a", "role": "operator", "kind": "api", "areas": []string{"jobs.read"}}, http.StatusUnprocessableEntity, "only assistant tokens have areas"},
		{"unknown area", map[string]any{"name": "a", "role": "operator", "kind": "assistant", "areas": []string{"everything"}}, http.StatusUnprocessableEntity, ""},
		{"area above role", map[string]any{"name": "a", "role": "viewer", "kind": "assistant", "areas": []string{"inventory.write"}}, http.StatusUnprocessableEntity, "inventory.write"},
		{"valid", map[string]any{"name": "a", "role": "operator", "kind": "assistant", "areas": []string{"inventory.write", "jobs.request"}}, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		resp := postJSON(t, c, url, tc.body)
		body := mustReadBody(t, resp)
		if resp.StatusCode != tc.want || !strings.Contains(body, tc.msg) {
			t.Errorf("%s: %d %s, want %d containing %q", tc.name, resp.StatusCode, body, tc.want, tc.msg)
		}
	}
}

func TestCreateAssistantTokenNeedsAnInteractiveSession(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	resp := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "a", "role": "operator", "kind": "assistant", "areas": []string{"jobs.read"}})
	if resp.Code != http.StatusForbidden {
		t.Fatalf("bearer-minted assistant token = %d, want 403", resp.Code)
	}
}

func TestTokenListShowsKindAndAreas(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("asst-list@example.test", "correct horse battery")
	s.SeedOrg("asst-list-org", owner)
	c := s.LoginAs(owner)
	url := s.HTTP.URL + "/api/v1/orgs/asst-list-org/tokens"
	if resp := postJSON(t, c, url, map[string]any{"name": "asst", "role": "operator", "kind": "assistant", "areas": []string{"repos.read"}}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, url, map[string]any{"name": "ci", "role": "viewer"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api: %d", resp.StatusCode)
	}
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	var listed struct {
		Tokens []struct {
			Name  string   `json:"name"`
			Kind  string   `json:"kind"`
			Areas []string `json:"areas"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tk := range listed.Tokens {
		got[tk.Name] = tk.Kind + ":" + strings.Join(tk.Areas, ",")
	}
	if got["asst"] != "assistant:repos.read" || got["ci"] != "api:" {
		t.Fatalf("list = %+v", got)
	}
}
