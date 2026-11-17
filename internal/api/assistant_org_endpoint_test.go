// The assistant endpoint per organization (backlog 3.100, spec
// docs/superpowers/specs/2026-11-17-personal-tokens-design.md rules 4 and
// 7), end to end: an organization assistant token keeps /api/mcp, a
// personal assistant token names its organization as /api/mcp/o/{org} (or
// uses /api/mcp with exactly one live grant), and the tools listed in an
// organization are the ones its effective role allows.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// mcpStatus sends initialize to path and returns the status and the problem's detail.
func mcpStatus(t *testing.T, s *apitest.Server, path, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.HTTP.URL+path, bytes.NewReader([]byte(mcpInitialize)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var p struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &p)
	return resp.StatusCode, p.Detail
}

func wantMCP(t *testing.T, s *apitest.Server, path, token string, status int, detail string) {
	t.Helper()
	got, d := mcpStatus(t, s, path, token)
	if got != status || (detail != "" && d != detail) {
		t.Fatalf("%s = %d %q, want %d %q", path, got, d, status, detail)
	}
}

type mcpBearer struct{ token string }

func (b mcpBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpConnect(t *testing.T, s *apitest.Server, path, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "pulsix-test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: s.HTTP.URL + path, HTTPClient: &http.Client{Transport: mcpBearer{token}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", path, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func mcpTools(t *testing.T, cs *sdk.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := map[string]bool{}
	for _, tl := range res.Tools {
		out[tl.Name] = true
	}
	return out
}

// mcpCallOK calls a tool that must succeed through the in-process REST dispatch.
func mcpCallOK(t *testing.T, cs *sdk.ClientSession, name string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		b, _ := json.Marshal(res.Content)
		t.Fatalf("%s returned a tool error: %s", name, b)
	}
}

var jobAreas = []tenancy.Area{tenancy.AreaJobsRead, tenancy.AreaJobsRequest}

const (
	severalOrgs    = "this token is valid for several organizations; connect to /api/mcp/o/<organization>"
	noOrganization = "the token is not valid for any organization"
	otherOrg       = "token is scoped to another organization"
	assistantOff   = "assistant access is turned off for this organization"
)

func TestAssistantPersonalTokenPerOrganization(t *testing.T) {
	f := newPersonalFixture(t, "asst-po")
	a := f.org(t, "asst-po-a", tenancy.RoleOperator)
	b := f.org(t, "asst-po-b", tenancy.RoleViewer)
	c := f.org(t, "asst-po-c", tenancy.RoleOperator)
	for _, o := range []uuid.UUID{a, b, c} {
		f.s.SetAssistantEnabled(t, o, true)
	}
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator, b: tenancy.RoleViewer}, jobAreas...)

	wantMCP(t, f.s, "/api/mcp", tok, http.StatusBadRequest, severalOrgs)
	wantMCP(t, f.s, "/api/mcp/o/asst-po-c", tok, http.StatusForbidden, otherOrg)
	wantMCP(t, f.s, "/api/mcp/o/no-such-org", tok, http.StatusNotFound, "organization not found")

	inA := mcpConnect(t, f.s, "/api/mcp/o/asst-po-a", tok)
	if names := mcpTools(t, inA); !names["request_run"] || !names["get_job_log"] {
		t.Fatalf("operator cap in A: %v", names)
	}
	mcpCallOK(t, inA, "list_jobs")
	inB := mcpConnect(t, f.s, "/api/mcp/o/asst-po-b", tok)
	if names := mcpTools(t, inB); names["request_run"] || !names["get_job_log"] {
		t.Fatalf("viewer cap in B: %v", names)
	}
	mcpCallOK(t, inB, "list_jobs")
}

func TestAssistantPersonalTokenSingleGrantAtBareEndpoint(t *testing.T) {
	f := newPersonalFixture(t, "asst-one")
	a := f.org(t, "asst-one-a", tenancy.RoleOperator)
	f.s.SetAssistantEnabled(t, a, true)
	tokenID, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator}, jobAreas...)
	cs := mcpConnect(t, f.s, "/api/mcp", tok)
	if names := mcpTools(t, cs); !names["request_run"] {
		t.Fatalf("single grant at /api/mcp: %v", names)
	}
	mcpCallOK(t, cs, "list_jobs")

	_, none := f.s.PersonalToken(t, f.user, nil, jobAreas...)
	wantMCP(t, f.s, "/api/mcp", none, http.StatusForbidden, noOrganization)

	// An admin's block leaves the token no live grant.
	if _, err := f.s.PersonalTokens.BlockGrant(db.WithOrgScope(context.Background(), a), tokenID, a, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_jobs", Arguments: map[string]any{}}); err == nil {
		t.Fatal("a call succeeded after the grant was blocked")
	}
	wantMCP(t, f.s, "/api/mcp", tok, http.StatusForbidden, noOrganization)
	wantMCP(t, f.s, "/api/mcp/o/asst-one-a", tok, http.StatusForbidden, otherOrg)
}

func TestAssistantPersonalTokenOrganizationSwitches(t *testing.T) {
	f := newPersonalFixture(t, "asst-sw")
	a := f.org(t, "asst-sw-a", tenancy.RoleOperator)
	b := f.org(t, "asst-sw-b", tenancy.RoleOperator)
	f.s.SetAssistantEnabled(t, a, true)
	f.s.SetAssistantEnabled(t, b, true)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator, b: tenancy.RoleOperator}, jobAreas...)

	f.s.SetAssistantEnabled(t, a, false)
	wantMCP(t, f.s, "/api/mcp/o/asst-sw-a", tok, http.StatusForbidden, assistantOff)
	wantMCP(t, f.s, "/api/mcp/o/asst-sw-b", tok, http.StatusOK, "")
	f.s.SetAssistantEnabled(t, a, true)

	if err := f.s.PersonalTokens.SetAllowed(db.WithOrgScope(context.Background(), a), a, false); err != nil {
		t.Fatal(err)
	}
	wantMCP(t, f.s, "/api/mcp/o/asst-sw-a", tok, http.StatusForbidden, "personal tokens are turned off for this organization")
	wantMCP(t, f.s, "/api/mcp/o/asst-sw-b", tok, http.StatusOK, "")
}

// TestAssistantPersonalTokenOpenSessionFollowsSwitches: a session connected
// while both switches are on is refused (403) on its next call once either is
// turned off, not only at the next connect. A refusal ends the client's
// session, so each switch gets its own.
func TestAssistantPersonalTokenOpenSessionFollowsSwitches(t *testing.T) {
	f := newPersonalFixture(t, "asst-open")
	a := f.org(t, "asst-open-a", tenancy.RoleOperator)
	f.s.SetAssistantEnabled(t, a, true)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator}, jobAreas...)

	for name, off := range map[string]func(){
		"assistant switch": func() { f.s.SetAssistantEnabled(t, a, false) },
		"personal token switch": func() {
			if err := f.s.PersonalTokens.SetAllowed(db.WithOrgScope(context.Background(), a), a, false); err != nil {
				t.Fatal(err)
			}
		},
	} {
		cs := mcpConnect(t, f.s, "/api/mcp/o/asst-open-a", tok)
		mcpCallOK(t, cs, "list_jobs")
		off()
		_, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_jobs", Arguments: map[string]any{}})
		if err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Fatalf("%s: call on the open session: %v", name, err)
		}
		f.s.SetAssistantEnabled(t, a, true)
		if err := f.s.PersonalTokens.SetAllowed(db.WithOrgScope(context.Background(), a), a, true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAssistantPersonalTokenFollowsMembership(t *testing.T) {
	f := newPersonalFixture(t, "asst-mem")
	a := f.org(t, "asst-mem-a", tenancy.RoleOperator)
	f.s.SetAssistantEnabled(t, a, true)
	_, tok := f.s.PersonalToken(t, f.user, map[uuid.UUID]tenancy.Role{a: tenancy.RoleOperator}, jobAreas...)
	path := "/api/mcp/o/asst-mem-a"
	if names := mcpTools(t, mcpConnect(t, f.s, path, tok)); !names["request_run"] {
		t.Fatalf("operator: %v", names)
	}
	scoped := db.WithOrgScope(context.Background(), a)
	if err := f.s.Deps.Tenancy.ChangeRole(scoped, a, tenancy.RoleOwner, f.owner, f.user, tenancy.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if names := mcpTools(t, mcpConnect(t, f.s, path, tok)); names["request_run"] || !names["get_job_log"] {
		t.Fatalf("demoted to viewer: %v", names)
	}
	if err := f.s.Deps.Tenancy.RemoveMember(scoped, a, tenancy.RoleOwner, f.owner, f.user); err != nil {
		t.Fatal(err)
	}
	wantMCP(t, f.s, path, tok, http.StatusForbidden, "")
	wantMCP(t, f.s, "/api/mcp", tok, http.StatusForbidden, noOrganization)
}

// TestAssistantOrgTokenAtOrgPaths: an organization assistant token keeps
// /api/mcp and may name its own organization, never another.
func TestAssistantOrgTokenAtOrgPaths(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	other, _ := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	wantMCP(t, s, "/api/mcp", tok, http.StatusOK, "")
	wantMCP(t, s, "/api/mcp/o/"+org.String(), tok, http.StatusOK, "")
	wantMCP(t, s, "/api/mcp/o/"+other.String(), tok, http.StatusForbidden, otherOrg)
	mcpCallOK(t, mcpConnect(t, s, "/api/mcp/o/"+org.String(), tok), "get_organization")
}

func TestAssistantOrgTokenToolsFollowLoweredMembership(t *testing.T) {
	f := newPersonalFixture(t, "asst-low")
	a := f.org(t, "asst-low-a", tenancy.RoleOperator)
	tok := f.s.AssistantTokenFor(t, a, f.user, tenancy.RoleOperator, jobAreas...)
	if names := mcpTools(t, mcpConnect(t, f.s, "/api/mcp", tok)); !names["request_run"] {
		t.Fatalf("operator: %v", names)
	}
	if err := f.s.Deps.Tenancy.ChangeRole(db.WithOrgScope(context.Background(), a), a, tenancy.RoleOwner, f.owner, f.user, tenancy.RoleViewer); err != nil {
		t.Fatal(err)
	}
	cs := mcpConnect(t, f.s, "/api/mcp", tok)
	if names := mcpTools(t, cs); names["request_run"] || !names["get_job_log"] {
		t.Fatalf("owner demoted to viewer: %v", names)
	}
	mcpCallOK(t, cs, "list_jobs")
	if err := f.s.Deps.Tenancy.RemoveMember(db.WithOrgScope(context.Background(), a), a, tenancy.RoleOwner, f.owner, f.user); err != nil {
		t.Fatal(err)
	}
	wantMCP(t, f.s, "/api/mcp", tok, http.StatusForbidden, "not a member of this organization")
}
