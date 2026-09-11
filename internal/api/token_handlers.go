package api

import (
	"context"
	"errors"
	"net/http"
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
		plain, tok, err := deps.Tokens.Create(ctx, scope.UserID, scope.OrgID, in.Body.Name, string(role), in.Body.ExpiresAt)
		if err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		setAuditObject(ctx, tok.ID.String(), "created API token "+tok.Name+" ("+tok.Role+")")
		out := &orgTokenCreateOutput{}
		out.Body.Token, out.Body.ID, out.Body.Name, out.Body.Role = plain, tok.ID.String(), tok.Name, tok.Role
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
				ID: tok.ID.String(), Name: tok.Name, Role: tok.Role,
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
}

// mapTokenErr is mapAuthErr plus one override, exactly like mapPasskeyErr:
// auth.ErrNotFound becomes 404 (an unknown token id, or one belonging to a
// different org) rather than mapAuthErr's 401 "login required", since here
// it never means "your own session is invalid".
func mapTokenErr(ctx context.Context, err error) error {
	if errors.Is(err, auth.ErrNotFound) {
		return huma.Error404NotFound("not found")
	}
	return mapAuthErr(ctx, err)
}
