package mcp_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestAnExpiredTokenIsRefused: expiry is checked on every call, so a token
// that lapses between tools/list and tools/call ends the session too.
func TestAnExpiredTokenIsRefused(t *testing.T) {
	s := apitest.New(t)
	org, asst := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	cs := mustConnect(t, s, asst)
	callOK(t, cs, "get_organization", nil, nil)
	if _, err := s.DB.Pool().Exec(context.Background(), `UPDATE api_tokens SET expires_at = now() - interval '1 hour' WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(context.Background(), toolParams("get_organization", nil)); err == nil {
		t.Fatal("a call succeeded with an expired token")
	}
	if resp := rawPost(t, s, asst, nil, []byte(initBody)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", resp.StatusCode)
	}
}

// lockedBuffer is a log sink the server's goroutines write to while the
// test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestTokensStayOutOfTheLogs captures what the endpoint logs through slog
// (the HTTP request logger is a separate standard logger that prints no
// headers, and the token travels only in the Authorization header) over a
// good call, a revoked-token call and a malformed-token
// call, and asserts no token plaintext reaches it. The server takes no
// injected logger, so the test swaps the default and restores it; it is not
// parallel for that reason.
func TestTokensStayOutOfTheLogs(t *testing.T) {
	s := apitest.New(t)
	_, asst := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	revokedOrg, revoked := s.OrgWithAssistantToken(t, tenancy.RoleOperator, tenancy.AreaInventoryRead)
	const malformed = "pxt_malformed-token-plaintext-0123456789"

	var sink lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	callOK(t, mustConnect(t, s, asst), "get_organization", nil, nil)

	rcs := mustConnect(t, s, revoked)
	toks, err := s.Deps.Tokens.List(db.WithOrgScope(context.Background(), revokedOrg), revokedOrg)
	if err != nil || len(toks) == 0 {
		t.Fatalf("tokens = %v %v", toks, err)
	}
	if err := s.Deps.Tokens.Revoke(db.WithOrgScope(context.Background(), revokedOrg), revokedOrg, toks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rcs.CallTool(context.Background(), toolParams("get_organization", nil)); err == nil {
		t.Fatal("a call succeeded with a revoked token")
	}
	if resp := rawPost(t, s, revoked, nil, []byte(initBody)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token = %d, want 401", resp.StatusCode)
	}
	if resp := rawPost(t, s, malformed, nil, []byte(initBody)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("malformed token = %d, want 401", resp.StatusCode)
	}

	// A store failure is the one path that logs: a cancelled context makes the
	// token lookup fail with an error that is not "unauthenticated", which the
	// endpoint logs. Without it the capture would see nothing to check.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(initBody)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+asst)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	s.HTTP.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("failed lookup = %d, want 401", rec.Code)
	}

	logged := sink.String()
	if logged == "" {
		t.Fatal("the capture saw no log line, so the test proves nothing")
	}
	for name, secret := range map[string]string{"good token": asst, "revoked token": revoked, "malformed token": malformed, "bearer header": "Bearer " + asst} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log contains the %s plaintext:\n%s", name, logged)
		}
	}
}
