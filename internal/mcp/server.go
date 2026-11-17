// Package mcp is the assistant endpoint (spec docs/superpowers/specs/
// 2026-11-16-assistant-access-design.md): an MCP server over stateless
// Streamable HTTP at /api/mcp and /api/mcp/o/{org}. Only an assistant token
// acting in an organization whose assistant access switch is on gets in:
// an organization token in its own organization, a personal token in one
// it holds a live grant for (spec 2026-11-17 personal tokens, rule 7). The
// tools listed are the ones the token's areas and its effective role there
// allow. Each tool dispatches to one
// fixed REST route in-process, through the manager's own router, so
// authorization (requireOrg with the token's areas), validation, secret
// masking and audit stay the REST layer's.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// MaxBodyBytes caps one request to the endpoint.
const MaxBodyBytes = 1 << 20

// instructions is what initialize tells the assistant.
const instructions = "Pulsix manages servers with Ansible: inventory (hosts, groups and their variables), " +
	"repositories, playbooks and job templates. Playbook runs need a person's approval in Pulsix: " +
	"request_run and request_retry create a request and return a link; give that link to the person " +
	"and wait for their approval, then read the run with get_job_log. A connection test " +
	"(start_connection_test) runs no playbook: it logs in, probes the host and pins nothing, so it " +
	"needs no approval. Secrets (keys, passwords, " +
	"secret variables) are set by a person in Pulsix, never through these tools."

// TokenResolver resolves a bearer token (satisfied by *auth.Tokens).
type TokenResolver interface {
	Resolve(ctx context.Context, plain string) (auth.Principal, auth.TokenScope, error)
}

// SwitchReader reads the assistant access switch.
type SwitchReader interface {
	Enabled(ctx context.Context, orgID uuid.UUID) (bool, error)
}

// OrgSlugs looks an organization's slug up by id.
type OrgSlugs interface {
	SlugOf(ctx context.Context, orgID uuid.UUID) (string, error)
}

// Deps is what New needs.
type Deps struct {
	// Router is the manager's own handler, which tools dispatch into. It
	// is read per request, so the handler that mounts the endpoint can
	// pass itself.
	Router    http.Handler
	Tokens    TokenResolver
	Assistant SwitchReader
	// Orgs names the organization a request was resolved to by id (an
	// organization token's own, a personal token's only grant).
	Orgs OrgSlugs
	// Access decides the organization and the token's effective role there.
	Access  OrgAccess
	Version string
	// PublicURL prefixes the approval links tools return.
	PublicURL string
}

type endpoint struct{ d Deps }

// New returns the endpoint's handler.
func New(d Deps) http.Handler { return &endpoint{d: d} }

func (e *endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// No browser has a reason to call this endpoint; refusing any Origin
	// also rules out DNS rebinding, as the MCP spec asks.
	if r.Header.Get("Origin") != "" {
		refuse(w, http.StatusForbidden, "cross-origin requests are refused")
		return
	}
	if r.ContentLength > MaxBodyBytes {
		refuse(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	header := r.Header.Get("Authorization")
	scheme, plain, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || plain == "" {
		refuse(w, http.StatusUnauthorized, "an assistant token is required")
		return
	}
	ctx := r.Context()
	p, scope, err := e.d.Tokens.Resolve(ctx, plain)
	if err != nil {
		if !errors.Is(err, auth.ErrUnauthenticated) {
			slog.ErrorContext(ctx, "assistant endpoint: resolve token", "err", err)
		}
		refuse(w, http.StatusUnauthorized, "an assistant token is required")
		return
	}
	if scope.Kind != auth.TokenKindAssistant {
		refuse(w, http.StatusForbidden, "this endpoint needs an assistant token")
		return
	}
	org, role, ok := e.authorize(w, r, p, scope)
	if !ok {
		return
	}

	areas := make([]tenancy.Area, len(scope.Areas))
	for i, a := range scope.Areas {
		areas[i] = tenancy.Area(a)
	}
	c := &caller{router: e.d.Router, authorization: header, slug: org.slug, areas: areas, publicURL: strings.TrimRight(e.d.PublicURL, "/")}
	srv := sdk.NewServer(&sdk.Implementation{Name: "pulsix", Title: "Pulsix", Version: e.d.Version}, &sdk.ServerOptions{Instructions: instructions})
	for _, t := range registry() {
		if t.allowedFor(areas, role) {
			t.register(srv, c)
		}
	}
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	h.ServeHTTP(w, r)
}

// authorize decides the organization of the request and the token's
// effective role there, and checks the organization's assistant switch. It
// answers the refusal itself when it returns false. The effective role is
// decided before the switch is read, so a token with no business in an
// organization learns nothing about its switch.
func (e *endpoint) authorize(w http.ResponseWriter, r *http.Request, p auth.Principal, scope auth.TokenScope) (target, tenancy.Role, bool) {
	ctx := r.Context()
	org, err := e.resolveOrg(r, p, scope)
	if err != nil {
		e.fail(ctx, w, "organization", err)
		return target{}, "", false
	}
	scoped := db.WithOrgScope(ctx, org.orgID)
	role, err := e.d.Access.EffectiveRole(scoped, p, scope, org.orgID)
	var refused Refusal
	if errors.As(err, &refused) {
		refuse(w, http.StatusForbidden, refused.Detail)
		return target{}, "", false
	}
	if err != nil {
		e.fail(ctx, w, "effective role", err)
		return target{}, "", false
	}
	on, err := e.d.Assistant.Enabled(scoped, org.orgID)
	if err != nil {
		e.fail(ctx, w, "read switch", err)
		return target{}, "", false
	}
	if !on {
		refuse(w, http.StatusForbidden, "assistant access is turned off for this organization")
		return target{}, "", false
	}
	if org.slug == "" {
		if org.slug, err = e.d.Orgs.SlugOf(scoped, org.orgID); err != nil {
			e.fail(ctx, w, "organization slug", err)
			return target{}, "", false
		}
	}
	return org, role, true
}

// fail answers a problem as itself and anything else as a logged 500.
func (e *endpoint) fail(ctx context.Context, w http.ResponseWriter, what string, err error) {
	var p problem
	if errors.As(err, &p) {
		refuse(w, p.status, p.detail)
		return
	}
	slog.ErrorContext(ctx, "assistant endpoint: "+what, "err", err)
	refuse(w, http.StatusInternalServerError, "internal error")
}

func refuse(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"title": http.StatusText(status), "status": status, "detail": detail})
}
