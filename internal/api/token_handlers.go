package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

type orgTokenCreateInput struct {
	// Org deliberately has no format:"uuid" tag, for the same reason
	// passkeyIDInput's ID doesn't: with the tag, huma rejects a malformed
	// value at the schema layer with 422 before the handler's own
	// uuid.Parse and 400 response ever run.
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
// It is written here, in Task 9, but deliberately called by nothing —
// registerRoutes in api.go does not invoke it, so none of these three
// routes are reachable on the running server yet. All three take an org id
// straight from the path and, today, gate only on requireMFAComplete: there
// is no check anywhere in this package yet that the calling principal is
// even a *member* of that org, let alone holds a role in it senior enough
// to mint, list or revoke its tokens. That check — comparing the caller's
// org_memberships role against what the operation requires, and (for
// Create) capping the new token's role at the caller's own — is Task 10's
// job: it is the same org-authorization layer every other org-scoped
// resource these routes will eventually sit alongside also needs, so
// duplicating a one-off version of it here would both pre-empt that design
// and leave three routes reachable by any authenticated caller for *any*
// org id. Task 10 mounts these three operations (adding its authorization
// in front of them) once that layer exists.
//
// Until then, the handlers themselves — argument parsing, the "shown once"
// plaintext contract, the metadata-only list shape, revoke's not-found
// mapping — are exercised directly against a standalone huma test harness
// with a hand-injected principal, deliberately bypassing both this
// function's absence from registerRoutes and any org-membership check, so
// the test proves nothing about authorization; see token_handlers_test.go.
func registerTokenRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/tokens", func(ctx context.Context, in *orgTokenCreateInput) (*orgTokenCreateOutput, error) {
		if _, err := requireMFAComplete(ctx); err != nil {
			return nil, err
		}
		orgID, err := uuid.Parse(in.Org)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid org id")
		}
		p, _ := auth.FromContext(ctx) // requireMFAComplete above already guarantees this is set.
		plain, tok, err := deps.Tokens.Create(ctx, p.UserID, orgID, in.Body.Name, in.Body.Role, in.Body.ExpiresAt)
		if err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		out := &orgTokenCreateOutput{}
		out.Body.Token, out.Body.ID, out.Body.Name, out.Body.Role = plain, tok.ID.String(), tok.Name, tok.Role
		out.Body.ExpiresAt, out.Body.CreatedAt = tok.ExpiresAt, tok.CreatedAt
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Get(hapi, "/api/v1/orgs/{org}/tokens", func(ctx context.Context, in *orgTokenListInput) (*orgTokenListOutput, error) {
		if _, err := requireMFAComplete(ctx); err != nil {
			return nil, err
		}
		orgID, err := uuid.Parse(in.Org)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid org id")
		}
		toks, err := deps.Tokens.List(ctx, orgID)
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
		if _, err := requireMFAComplete(ctx); err != nil {
			return nil, err
		}
		orgID, err := uuid.Parse(in.Org)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid org id")
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		if err := deps.Tokens.Revoke(ctx, orgID, id); err != nil {
			return nil, mapTokenErr(ctx, err)
		}
		return &okOutput{}, nil
	})
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
