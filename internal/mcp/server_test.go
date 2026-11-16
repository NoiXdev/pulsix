package mcp_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func rawPost(t *testing.T, s *apitest.Server, token string, header map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.HTTP.URL+"/api/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func TestEndpointRefusals(t *testing.T) {
	s := apitest.New(t)
	org, asst := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	_, api := s.OrgWithToken(t)

	if resp := rawPost(t, s, "", nil, []byte(initBody)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", resp.StatusCode)
	}
	if resp := rawPost(t, s, "pxt_bogus", nil, []byte(initBody)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", resp.StatusCode)
	}
	if resp := rawPost(t, s, api, nil, []byte(initBody)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("api token = %d, want 403", resp.StatusCode)
	}
	if resp := rawPost(t, s, asst, map[string]string{"Origin": "https://evil.example"}, []byte(initBody)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("Origin header = %d, want 403", resp.StatusCode)
	}
	big := append([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"x":"`), bytes.Repeat([]byte("a"), 1<<20+1)...)
	big = append(big, []byte(`"}}`)...)
	if resp := rawPost(t, s, asst, nil, big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", resp.StatusCode)
	}
	if resp := rawPost(t, s, asst, nil, []byte(initBody)); resp.StatusCode != http.StatusOK {
		t.Errorf("valid initialize = %d, want 200", resp.StatusCode)
	}
	s.SetAssistantEnabled(t, org, false)
	if resp := rawPost(t, s, asst, nil, []byte(initBody)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("switch off = %d, want 403", resp.StatusCode)
	}
}

func TestInitializeNamesTheServerAndTheApproval(t *testing.T) {
	s := apitest.New(t)
	_, asst := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	cs := mustConnect(t, s, asst)
	init := cs.InitializeResult()
	if init.ServerInfo.Name != "pulsix" || !strings.Contains(init.Instructions, "approval") {
		t.Fatalf("initialize = %+v", init)
	}
}

func TestToolListFollowsTheAreas(t *testing.T) {
	s := apitest.New(t)
	_, inv := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	if names := toolNames(t, mustConnect(t, s, inv)); !names["get_organization"] || names["list_jobs"] {
		t.Fatalf("inventory.read tools = %v", names)
	}
	_, jobsOnly := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaJobsRead)
	names := toolNames(t, mustConnect(t, s, jobsOnly))
	if names["get_organization"] {
		t.Fatalf("jobs.read token sees get_organization: %v", names)
	}
	cs := mustConnect(t, s, jobsOnly)
	if _, err := cs.CallTool(context.Background(), toolParams("get_organization", nil)); err == nil {
		text, isErr := call(t, cs, "get_organization", nil)
		if !isErr {
			t.Fatalf("a hidden tool ran: %s", text)
		}
	}
}

func TestGetOrganization(t *testing.T) {
	s := apitest.New(t)
	org, asst := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	var out struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	callOK(t, mustConnect(t, s, asst), "get_organization", nil, &out)
	if out.Slug != org.String() || out.Name == "" {
		t.Fatalf("get_organization = %+v", out)
	}
}

// TestEveryCallReauthenticates: a revoked token or a switch turned off
// between tools/list and tools/call ends the session (Review Focus 4).
func TestEveryCallReauthenticates(t *testing.T) {
	s := apitest.New(t)
	org, asst := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	cs := mustConnect(t, s, asst)
	callOK(t, cs, "get_organization", nil, nil)
	s.SetAssistantEnabled(t, org, false)
	if _, err := cs.CallTool(context.Background(), toolParams("get_organization", nil)); err == nil {
		t.Fatal("a call succeeded with the switch off")
	}
	s.SetAssistantEnabled(t, org, true)
	callOK(t, mustConnect(t, s, asst), "get_organization", nil, nil)
	toks, err := s.Deps.Tokens.List(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(toks) == 0 {
		t.Fatalf("tokens = %v %v", toks, err)
	}
	if err := s.Deps.Tokens.Revoke(db.WithOrgScope(context.Background(), org), org, toks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(context.Background(), toolParams("get_organization", nil)); err == nil {
		t.Fatal("a call succeeded with a revoked token")
	}
}
