package mcp_test

// The endpoint's organization rules (spec 2026-11-17 personal tokens,
// rules 4 and 7) against fakes: which organization a request is for, which
// refusal it gets, and which tools it is shown there.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/mcp"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type resolvedToken struct {
	p auth.Principal
	s auth.TokenScope
}

type fakeResolver map[string]resolvedToken

func (f fakeResolver) Resolve(_ context.Context, plain string) (auth.Principal, auth.TokenScope, error) {
	r, ok := f[plain]
	if !ok {
		return auth.Principal{}, auth.TokenScope{}, auth.ErrUnauthenticated
	}
	return r.p, r.s, nil
}

// fakeSwitch is the assistant switch; it fails unless bound to the
// organization it is asked about, as row-level security would.
type fakeSwitch struct{ off map[uuid.UUID]bool }

func (f fakeSwitch) Enabled(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if bound, ok := db.OrgScope(ctx); !ok || bound != orgID {
		return false, errors.New("switch read outside its organization")
	}
	return !f.off[orgID], nil
}

type fakeSlugs map[uuid.UUID]string

func (f fakeSlugs) SlugOf(_ context.Context, orgID uuid.UUID) (string, error) {
	if s, ok := f[orgID]; ok {
		return s, nil
	}
	return "", tenancy.ErrNotFound
}

// fakeAccess holds the effective role per token and organization. It
// enforces OrgAccess's preconditions: SingleGrant unbound, EffectiveRole
// bound to the organization.
type fakeAccess struct {
	slugs    map[string]uuid.UUID
	personal map[uuid.UUID]map[uuid.UUID]tenancy.Role // token → org → effective role
	org      map[uuid.UUID]tenancy.Role               // an organization token's effective role
	fail     string                                   // the method that fails
}

func (f fakeAccess) OrgBySlug(_ context.Context, slug string) (uuid.UUID, error) {
	if f.fail == "OrgBySlug" {
		return uuid.Nil, errors.New("database down")
	}
	id, ok := f.slugs[slug]
	if !ok {
		return uuid.Nil, tenancy.ErrNotFound
	}
	return id, nil
}

func (f fakeAccess) SingleGrant(ctx context.Context, tokenID uuid.UUID) (uuid.UUID, int, error) {
	if _, bound := db.OrgScope(ctx); bound || f.fail == "SingleGrant" {
		return uuid.Nil, 0, errors.New("grant count failed")
	}
	grants := f.personal[tokenID]
	if len(grants) != 1 {
		return uuid.Nil, len(grants), nil
	}
	for org := range grants {
		return org, 1, nil
	}
	return uuid.Nil, 0, nil
}

func (f fakeAccess) EffectiveRole(ctx context.Context, p auth.Principal, s auth.TokenScope, orgID uuid.UUID) (tenancy.Role, error) {
	if bound, ok := db.OrgScope(ctx); !ok || bound != orgID || f.fail == "EffectiveRole" {
		return "", errors.New("effective role failed")
	}
	if !s.Personal {
		if s.OrgID != orgID {
			return "", mcp.Refusal{Detail: "token is scoped to another organization"}
		}
		return f.org[orgID], nil
	}
	role, ok := f.personal[*p.TokenID][orgID]
	if !ok {
		return "", mcp.Refusal{Detail: "token is scoped to another organization"}
	}
	return role, nil
}

// recordingRouter stands in for the REST router: it records the paths
// tools dispatch to and answers every one with an empty object.
type recordingRouter struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"org":{"slug":"x"},"jobs":[]}`))
}

func (r *recordingRouter) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.paths) == 0 {
		return ""
	}
	return r.paths[len(r.paths)-1]
}

// orgEnv: organizations a and b; a personal token with grants in both
// (operator in a, viewer in b), one with a grant in a only, one with none,
// a personal API token, and an organization assistant token of a.
type orgEnv struct {
	url                         string
	a, b                        uuid.UUID
	router                      *recordingRouter
	access                      *fakeAccess
	sw                          *fakeSwitch
	both, single, none, api, ot string
}

var envAreas = []string{string(tenancy.AreaInventoryRead), string(tenancy.AreaJobsRead), string(tenancy.AreaJobsRequest)}

func newOrgEnv(t *testing.T) *orgEnv {
	t.Helper()
	e := &orgEnv{a: uuid.New(), b: uuid.New(), router: &recordingRouter{},
		both: "pxp_both", single: "pxp_single", none: "pxp_none", api: "pxp_api", ot: "pxt_org"}
	ids := map[string]uuid.UUID{}
	tokens := fakeResolver{}
	personal := func(plain string, kind auth.TokenKind) uuid.UUID {
		id := uuid.New()
		ids[plain] = id
		s := auth.TokenScope{Personal: true, Kind: kind}
		if kind == auth.TokenKindAssistant {
			s.Areas = envAreas
		}
		tokens[plain] = resolvedToken{p: auth.Principal{UserID: uuid.New(), TokenID: &id, MFAComplete: true}, s: s}
		return id
	}
	both := personal(e.both, auth.TokenKindAssistant)
	single := personal(e.single, auth.TokenKindAssistant)
	personal(e.none, auth.TokenKindAssistant)
	apiTok := personal(e.api, auth.TokenKindAPI)
	otID := uuid.New()
	tokens[e.ot] = resolvedToken{p: auth.Principal{UserID: uuid.New(), TokenID: &otID, MFAComplete: true},
		s: auth.TokenScope{OrgID: e.a, Role: string(tenancy.RoleOperator), Kind: auth.TokenKindAssistant, Areas: envAreas}}
	e.access = &fakeAccess{
		slugs: map[string]uuid.UUID{"a": e.a, "b": e.b},
		personal: map[uuid.UUID]map[uuid.UUID]tenancy.Role{
			both:   {e.a: tenancy.RoleOperator, e.b: tenancy.RoleViewer},
			single: {e.a: tenancy.RoleOperator},
			apiTok: {e.a: tenancy.RoleOperator},
		},
		org: map[uuid.UUID]tenancy.Role{e.a: tenancy.RoleOperator},
	}
	e.sw = &fakeSwitch{off: map[uuid.UUID]bool{}}
	h := mcp.New(mcp.Deps{Router: e.router, Tokens: tokens, Assistant: e.sw, Orgs: fakeSlugs{e.a: "a", e.b: "b"},
		Access: e.access, Version: "test"})
	// The handler reads the fakes per request, so a test may change them
	// after the server started.
	r := chi.NewRouter()
	r.Handle("/api/mcp", h)
	r.Handle("/api/mcp/o/{org}", h)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

// post sends initialize to path and returns the status and the problem's detail.
func (e *orgEnv) post(t *testing.T, path, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.url+path, bytes.NewReader([]byte(initBody)))
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

func (e *orgEnv) connect(t *testing.T, path, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "pulsix-test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   e.url + path,
		HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", path, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func wantRefusal(t *testing.T, e *orgEnv, path, token string, status int, detail string) {
	t.Helper()
	got, d := e.post(t, path, token)
	if got != status || d != detail {
		t.Fatalf("%s = %d %q, want %d %q", path, got, d, status, detail)
	}
}

func TestPersonalTokenWithSeveralGrantsNeedsOrgPath(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp", e.both, http.StatusBadRequest,
		"this token is valid for several organizations; connect to /api/mcp/o/<organization>")
}

func TestPersonalTokenWithOneGrantWorksAtBareEndpoint(t *testing.T) {
	e := newOrgEnv(t)
	cs := e.connect(t, "/api/mcp", e.single)
	if names := toolNames(t, cs); !names["request_run"] || !names["get_organization"] {
		t.Fatalf("tools = %v", names)
	}
	callOK(t, cs, "get_organization", nil, nil)
	if got := e.router.last(); got != "/api/v1/orgs/a" {
		t.Fatalf("dispatched to %q, want the single grant's organization", got)
	}
}

func TestPersonalTokenWithoutGrantRefusedAtBareEndpoint(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp", e.none, http.StatusForbidden, "the token is not valid for any organization")
	wantRefusal(t, e, "/api/mcp/o/a", e.none, http.StatusForbidden, "token is scoped to another organization")
}

func TestPersonalTokenAtOrgPathWithoutGrant(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp/o/b", e.single, http.StatusForbidden, "token is scoped to another organization")
	if got, _ := e.post(t, "/api/mcp/o/a", e.single); got != http.StatusOK {
		t.Fatalf("own grant's path = %d, want 200", got)
	}
}

// TestNoGrantHidesTheSwitch: a token with no business in an organization
// learns nothing about its assistant switch.
func TestNoGrantHidesTheSwitch(t *testing.T) {
	e := newOrgEnv(t)
	e.sw.off[e.b] = true
	wantRefusal(t, e, "/api/mcp/o/b", e.single, http.StatusForbidden, "token is scoped to another organization")
}

func TestOrgTokenAtOtherOrgPath(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp/o/b", e.ot, http.StatusForbidden, "token is scoped to another organization")
	for _, path := range []string{"/api/mcp", "/api/mcp/o/a"} {
		cs := e.connect(t, path, e.ot)
		callOK(t, cs, "get_organization", nil, nil)
		if got := e.router.last(); got != "/api/v1/orgs/a" {
			t.Fatalf("%s dispatched to %q", path, got)
		}
	}
}

func TestUnknownOrgPath(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp/o/nope", e.both, http.StatusNotFound, "organization not found")
	wantRefusal(t, e, "/api/mcp/o/nope", e.ot, http.StatusNotFound, "organization not found")
}

func TestPersonalAPITokenRefusedAtOrgPath(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp/o/a", e.api, http.StatusForbidden, "this endpoint needs an assistant token")
}

func TestUnauthenticatedAtOrgPath(t *testing.T) {
	e := newOrgEnv(t)
	wantRefusal(t, e, "/api/mcp/o/nope", "pxp_bogus", http.StatusUnauthorized, "an assistant token is required")
}

func TestAssistantSwitchAppliesPerOrganization(t *testing.T) {
	e := newOrgEnv(t)
	e.sw.off[e.a] = true
	wantRefusal(t, e, "/api/mcp/o/a", e.both, http.StatusForbidden, "assistant access is turned off for this organization")
	wantRefusal(t, e, "/api/mcp", e.single, http.StatusForbidden, "assistant access is turned off for this organization")
	wantRefusal(t, e, "/api/mcp", e.ot, http.StatusForbidden, "assistant access is turned off for this organization")
	if got, _ := e.post(t, "/api/mcp/o/b", e.both); got != http.StatusOK {
		t.Fatalf("switch on in b = %d, want 200", got)
	}
}

func TestToolsListedPerEffectiveRole(t *testing.T) {
	e := newOrgEnv(t)
	a := toolNames(t, e.connect(t, "/api/mcp/o/a", e.both))
	b := toolNames(t, e.connect(t, "/api/mcp/o/b", e.both))
	if !a["request_run"] || !a["get_job_log"] {
		t.Fatalf("operator in a: %v", a)
	}
	if b["request_run"] || !b["get_job_log"] {
		t.Fatalf("viewer in b: %v", b)
	}
	callOK(t, e.connect(t, "/api/mcp/o/b", e.both), "get_organization", nil, nil)
	if got := e.router.last(); got != "/api/v1/orgs/b" {
		t.Fatalf("dispatched to %q", got)
	}
}

func TestOrgTokenToolsFollowEffectiveRole(t *testing.T) {
	e := newOrgEnv(t)
	e.access.org[e.a] = tenancy.RoleViewer
	if names := toolNames(t, e.connect(t, "/api/mcp", e.ot)); names["request_run"] || !names["get_job_log"] {
		t.Fatalf("viewer org token: %v", names)
	}
}

// TestAccessFailuresFailClosed: a failure to decide is a 500 that names
// nothing.
func TestAccessFailuresFailClosed(t *testing.T) {
	cases := []struct{ fail, path, token string }{
		{"OrgBySlug", "/api/mcp/o/a", "both"},
		{"SingleGrant", "/api/mcp", "single"},
		{"EffectiveRole", "/api/mcp/o/a", "both"},
		{"EffectiveRole", "/api/mcp", "ot"},
	}
	for _, tc := range cases {
		e := newOrgEnv(t)
		e.access.fail = tc.fail
		tok := map[string]string{"both": e.both, "single": e.single, "ot": e.ot}[tc.token]
		status, detail := e.post(t, tc.path, tok)
		if status != http.StatusInternalServerError || detail != "internal error" || strings.Contains(detail, "failed") {
			t.Errorf("%s failing at %s = %d %q, want 500", tc.fail, tc.path, status, detail)
		}
	}
}
