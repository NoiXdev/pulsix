package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgTokenCreateInput struct {
	// Org is the organization's slug, exactly like every other org-scoped
	// route's {org} path parameter (org_handlers.go, member_handlers.go) —
	// not a raw id. requireOrg resolves it.
	Org  string `path:"org"`
	Body struct {
		Name string `json:"name" minLength:"1"`
		// Role's enum tag mirrors the org_role Postgres enum
		// api_tokens.role is constrained to
		// (internal/db/migrations/00006_api_tokens.sql), so a bad value is
		// rejected at the schema layer with 400 instead of surfacing as a
		// raw, unmapped enum-constraint error from the database (which
		// mapAuthErr would otherwise turn into a generic 500).
		Role      string     `json:"role" enum:"owner,admin,operator,viewer"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		// Kind and Areas (spec 2026-11-16 assistant access): an assistant
		// token works only at the assistant endpoint and within its areas.
		Kind  string   `json:"kind,omitempty" enum:"api,assistant" doc:"api (the default) or assistant"`
		Areas []string `json:"areas,omitempty" doc:"Assistant tokens only: the areas the token may use (inventory.read, inventory.write, repos.read, repos.write, templates.read, templates.write, credentials.names, jobs.read, jobs.request, monitoring.read, alerts.handle, activity.read)"`
	}
}

type orgTokenCreateOutput struct {
	Body struct {
		// Token is the plaintext bearer credential. It is returned exactly
		// once, by this call only: Tokens.Create never persists it (only
		// its SHA-256 hash), so there is no later endpoint — not even
		// orgTokenListOutput below — that could ever show it again.
		Token     string     `json:"token"`
		ID        string     `json:"id"`
		Name      string     `json:"name"`
		Role      string     `json:"role"`
		Kind      string     `json:"kind"`
		Areas     []string   `json:"areas,omitempty"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		CreatedAt time.Time  `json:"created_at"`
	}
}

// tokenDTO is the metadata-only view List returns: everything
// orgTokenCreateOutput.Body carries except Token, the one field that must
// never appear anywhere but the single Create response.
type tokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	Kind       string     `json:"kind" enum:"api,assistant"`
	Areas      []string   `json:"areas,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type orgTokenListInput struct {
	Org string `path:"org"`
}

type orgTokenListOutput struct {
	Body struct {
		Tokens []tokenDTO `json:"tokens"`
	}
}

type orgTokenIDInput struct {
	Org string `path:"org"`
	// ID deliberately has no format:"uuid" tag either, matching Org above.
	ID string `path:"id"`
}

type orgTokenUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Name *string `json:"name,omitempty" minLength:"1" doc:"The new name"`
		// Areas is a pointer so that a present key, even an empty list, counts
		// as a change request (an empty list is then refused).
		Areas        *[]string  `json:"areas,omitempty" doc:"Assistant tokens only: replaces the areas"`
		ExpiresAt    *time.Time `json:"expires_at,omitempty" doc:"A new expiry; must be in the future"`
		NeverExpires bool       `json:"never_expires,omitempty" doc:"Remove the expiry"`
	}
}

type orgTokenUpdateOutput struct {
	Body struct {
		Token tokenDTO `json:"token"`
	}
}

// registerTokenRoutes wires org-scoped API token management: create (which
// returns the plaintext once), list (metadata only) and revoke.
//
// Written in Task 9 but left unmounted, because none of the three checked
// anything beyond requireMFAComplete: there was no check that the caller
// was even a member of the org in the path, let alone senior enough in it
// to mint, list or revoke its tokens. Task 10 adds that check — requireOrg,
// gating on tenancy.PermTokensManage — and mounts these on the bearer-
// enabled *huma.Group in api.go (all three consult
// auth.TokenScopeFromContext by way of requireOrg, which is exactly the
// subtree that group exists for).
//
// Create additionally requires an interactive session
// (requireInteractiveSession): a bearer token can list and revoke tokens
// but never mint one, so a leaked credential cannot outlive its own
// revocation through a successor it created.
//
// Create also caps the minted token's role at the caller's own
// effective role (tenancy.Lower(scope.Role, requested)): a token can never
// be more privileged than the principal that created it, the same
// never-widen rule requireOrg itself applies to an existing token's scope.
func registerTokenRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/tokens", func(ctx context.Context, in *orgTokenCreateInput) (*orgTokenCreateOutput, error) {
		// Credentials do not mint credentials: creating a token requires an
		// interactive, MFA-complete browser session and refuses any request
		// that authenticated with a bearer token, whatever its scope or
		// role. Otherwise a leaked token with an expiry could mint a
		// successor without one, and revoking the leaked token would leave
		// what it minted untouched. Listing and revoking stay available to
		// tokens with PermTokensManage.
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		role := tenancy.Lower(scope.Role, tenancy.Role(in.Body.Role))
		kind := auth.TokenKind(in.Body.Kind)
		if kind == "" {
			kind = auth.TokenKindAPI
		}
		if err := validateTokenAreas(kind, role, in.Body.Areas); err != nil {
			return nil, err
		}
		plain, tok, err := deps.Tokens.CreateWith(ctx, scope.UserID, scope.OrgID, auth.NewToken{
			Name: in.Body.Name, Role: string(role), Kind: kind, Areas: in.Body.Areas, ExpiresAt: in.Body.ExpiresAt,
		})
		if err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		summary := "created API token " + tok.Name + " (" + tok.Role + ")"
		if kind == auth.TokenKindAssistant {
			summary = "created assistant token " + tok.Name + " (" + tok.Role + "; " + strings.Join(tok.Areas, ", ") + ")"
		}
		setAuditObject(ctx, tok.ID.String(), summary)
		out := &orgTokenCreateOutput{}
		out.Body.Token, out.Body.ID, out.Body.Name, out.Body.Role = plain, tok.ID.String(), tok.Name, tok.Role
		out.Body.Kind, out.Body.Areas = string(tok.Kind), tok.Areas
		out.Body.ExpiresAt, out.Body.CreatedAt = tok.ExpiresAt, tok.CreatedAt
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createToken", "token"))

	huma.Get(hapi, "/api/v1/orgs/{org}/tokens", func(ctx context.Context, in *orgTokenListInput) (*orgTokenListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		toks, err := deps.Tokens.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		out := &orgTokenListOutput{}
		out.Body.Tokens = make([]tokenDTO, len(toks))
		for i, tok := range toks {
			out.Body.Tokens[i] = tokenDTO{
				ID: tok.ID.String(), Name: tok.Name, Role: tok.Role, Kind: string(tok.Kind), Areas: tok.Areas,
				ExpiresAt: tok.ExpiresAt, LastUsedAt: tok.LastUsedAt, CreatedAt: tok.CreatedAt,
			}
		}
		return out, nil
	})

	huma.Delete(hapi, "/api/v1/orgs/{org}/tokens/{id}", func(ctx context.Context, in *orgTokenIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		if err := deps.Tokens.Revoke(ctx, scope.OrgID, id); err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "revoked API token")
		return &okOutput{}, nil
	}, withAudit("revokeToken", "token"))

	// Editing a token needs an interactive session like creating one: a
	// bearer token must not be able to widen its own areas or lengthen its
	// own life.
	huma.Patch(hapi, "/api/v1/orgs/{org}/tokens/{id}", func(ctx context.Context, in *orgTokenUpdateInput) (*orgTokenUpdateOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		if in.Body.ExpiresAt != nil && in.Body.NeverExpires {
			return nil, huma.Error422UnprocessableEntity("expires_at and never_expires exclude each other")
		}
		tok, err := deps.Tokens.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		change := auth.TokenChange{Name: in.Body.Name, Areas: in.Body.Areas}
		switch {
		case in.Body.ExpiresAt != nil:
			change.Expiry, change.ExpiresAt = auth.ExpirySet, in.Body.ExpiresAt
		case in.Body.NeverExpires:
			change.Expiry = auth.ExpiryClear
		}
		if in.Body.Areas != nil {
			role := tenancy.Lower(scope.Role, tenancy.Role(tok.Role))
			if err := validateTokenAreas(tok.Kind, role, *in.Body.Areas); err != nil {
				return nil, err
			}
		}
		before, after, err := deps.Tokens.Update(ctx, scope.OrgID, id, change)
		if err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), tokenChangeSummary(before, after))
		out := &orgTokenUpdateOutput{}
		out.Body.Token = tokenDTO{
			ID: after.ID.String(), Name: after.Name, Role: after.Role, Kind: string(after.Kind), Areas: after.Areas,
			ExpiresAt: after.ExpiresAt, LastUsedAt: after.LastUsedAt, CreatedAt: after.CreatedAt,
		}
		return out, nil
	}, withAudit("updateToken", "token"))
}

// tokenChangeSummary words an update for the activity feed: the old and the
// new value of everything that differs.
func tokenChangeSummary(before, after auth.Token) string {
	head := "changed API token "
	if before.Kind == auth.TokenKindAssistant {
		head = "changed assistant token "
	}
	var parts []string
	if before.Name != after.Name {
		parts = append(parts, "name "+before.Name+" → "+after.Name)
	}
	if strings.Join(before.Areas, ", ") != strings.Join(after.Areas, ", ") {
		parts = append(parts, "areas "+strings.Join(before.Areas, ", ")+" → "+strings.Join(after.Areas, ", "))
	}
	if expiryText(before.ExpiresAt) != expiryText(after.ExpiresAt) {
		parts = append(parts, "expiry "+expiryText(before.ExpiresAt)+" → "+expiryText(after.ExpiresAt))
	}
	if len(parts) == 0 {
		parts = []string{"no change"}
	}
	return head + before.Name + ": " + strings.Join(parts, "; ")
}

func expiryText(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

// validateTokenAreas enforces the token create body's kind/areas shape: an
// assistant token needs at least one known, distinct area, each fully
// covered by the token's capped role (so no area is dead weight), and an
// api token has none.
func validateTokenAreas(kind auth.TokenKind, role tenancy.Role, areas []string) error {
	if kind != auth.TokenKindAssistant {
		if len(areas) > 0 {
			return huma.Error422UnprocessableEntity("only assistant tokens have areas")
		}
		return nil
	}
	if len(areas) == 0 {
		return huma.Error422UnprocessableEntity("an assistant token needs at least one area")
	}
	seen := map[string]bool{}
	for _, a := range areas {
		if !tenancy.ValidArea(tenancy.Area(a)) {
			return huma.Error422UnprocessableEntity("unknown area " + strconv.Quote(a))
		}
		if seen[a] {
			return huma.Error422UnprocessableEntity("area " + a + " is listed twice")
		}
		seen[a] = true
		if !tenancy.RoleAllowsArea(role, tenancy.Area(a)) {
			return huma.Error422UnprocessableEntity("area " + a + " needs more than the token's role " + string(role) + " allows")
		}
	}
	return nil
}

// mapTokenErr is mapAuthErr plus two overrides (ErrTokenExpired becomes 409), like mapPasskeyErr:
// auth.ErrNotFound becomes 404 (an unknown token id, or one belonging to a
// different org) rather than mapAuthErr's 401 "login required", since here
// it never means "your own session is invalid".
func mapTokenErr(ctx context.Context, err error) error {
	if errors.Is(err, auth.ErrNotFound) {
		return huma.Error404NotFound("not found")
	}
	if errors.Is(err, auth.ErrTokenExpired) {
		return huma.Error409Conflict(err.Error())
	}
	return mapAuthErr(ctx, err)
}
