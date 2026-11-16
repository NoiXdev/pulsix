// Package mcp is the assistant endpoint (spec docs/superpowers/specs/
// 2026-11-16-assistant-access-design.md): an MCP server over stateless
// Streamable HTTP at /api/mcp. Only an assistant token of an organization
// whose assistant access switch is on gets in. Each tool dispatches to one
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
	"repositories, playbooks and job templates. Runs need a person's approval in Pulsix: " +
	"request_run and request_retry create a request and return a link; give that link to the person " +
	"and wait for their approval, then read the run with get_job_log. Secrets (keys, passwords, " +
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
	Orgs      OrgSlugs
	Version   string
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
	_, scope, err := e.d.Tokens.Resolve(ctx, plain)
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
	scoped := db.WithOrgScope(ctx, scope.OrgID)
	on, err := e.d.Assistant.Enabled(scoped, scope.OrgID)
	if err != nil {
		slog.ErrorContext(ctx, "assistant endpoint: read switch", "err", err)
		refuse(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !on {
		refuse(w, http.StatusForbidden, "assistant access is turned off for this organization")
		return
	}
	slug, err := e.d.Orgs.SlugOf(scoped, scope.OrgID)
	if err != nil {
		slog.ErrorContext(ctx, "assistant endpoint: organization", "err", err)
		refuse(w, http.StatusInternalServerError, "internal error")
		return
	}

	c := &caller{router: e.d.Router, authorization: header, slug: slug, scope: scope, publicURL: strings.TrimRight(e.d.PublicURL, "/")}
	srv := sdk.NewServer(&sdk.Implementation{Name: "pulsix", Title: "Pulsix", Version: e.d.Version}, &sdk.ServerOptions{Instructions: instructions})
	areas := make([]tenancy.Area, len(scope.Areas))
	for i, a := range scope.Areas {
		areas[i] = tenancy.Area(a)
	}
	for _, t := range registry() {
		if t.allowed(areas) {
			t.register(srv, c)
		}
	}
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	h.ServeHTTP(w, r)
}

func refuse(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"title": http.StatusText(status), "status": status, "detail": detail})
}
