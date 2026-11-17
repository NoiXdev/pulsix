package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// OrgAccess decides the organization of an assistant request and the
// token's effective role there (spec 2026-11-17 personal tokens, rules 2,
// 4 and 7).
type OrgAccess interface {
	// OrgBySlug is the organization a path names; tenancy.ErrNotFound when
	// there is none.
	OrgBySlug(ctx context.Context, slug string) (uuid.UUID, error)
	// SingleGrant counts a personal token's live grants; orgID is the one
	// organization when n == 1. ctx must not be bound to an organization.
	SingleGrant(ctx context.Context, tokenID uuid.UUID) (orgID uuid.UUID, n int, err error)
	// EffectiveRole is what the token may do in orgID: the lower of the
	// owner's current membership role and the token's role (an
	// organization token's own, a personal token's grant cap). A Refusal
	// when the token may not act there at all; any other error is a
	// failure to decide. ctx is bound to orgID.
	EffectiveRole(ctx context.Context, p auth.Principal, scope auth.TokenScope, orgID uuid.UUID) (tenancy.Role, error)
}

// Refusal is OrgAccess.EffectiveRole's answer when the token may not act in
// the organization: the endpoint answers 403 with Detail.
type Refusal struct{ Detail string }

func (r Refusal) Error() string { return r.Detail }

// The endpoint's refusals about the organization. The connect script and
// the user documentation quote them.
const (
	orgNotFound    = "organization not found"
	tokenOtherOrg  = "token is scoped to another organization"
	noOrganization = "the token is not valid for any organization"
	severalOrgs    = "this token is valid for several organizations; connect to /api/mcp/o/<organization>"
	// OrgPathPattern is the path a personal assistant token names its
	// organization with.
	OrgPathPattern = "/api/mcp/o/{org}"
)

// problem is a refusal with its status; anything else resolveOrg returns is
// a failure to decide.
type problem struct {
	status int
	detail string
}

func (p problem) Error() string { return p.detail }

// target is the organization one request acts in.
type target struct {
	orgID uuid.UUID
	slug  string // empty: look it up by orgID
}

// resolveOrg decides which organization the request is for (spec rule 7):
// the one the path names, else an organization token's own, else a
// personal token's only live grant. It runs before anything is bound to
// an organization.
func (e *endpoint) resolveOrg(r *http.Request, p auth.Principal, scope auth.TokenScope) (target, error) {
	ctx := r.Context()
	if slug := chi.URLParam(r, "org"); slug != "" {
		id, err := e.d.Access.OrgBySlug(ctx, slug)
		if errors.Is(err, tenancy.ErrNotFound) {
			return target{}, problem{http.StatusNotFound, orgNotFound}
		}
		if err != nil {
			return target{}, fmt.Errorf("organization by slug: %w", err)
		}
		if !scope.Personal && scope.OrgID != id {
			return target{}, problem{http.StatusForbidden, tokenOtherOrg}
		}
		return target{orgID: id, slug: slug}, nil
	}
	if !scope.Personal {
		return target{orgID: scope.OrgID}, nil
	}
	if p.TokenID == nil {
		return target{}, errors.New("personal token without a token id")
	}
	orgID, n, err := e.d.Access.SingleGrant(ctx, *p.TokenID)
	switch {
	case err != nil:
		return target{}, fmt.Errorf("count grants: %w", err)
	case n == 0:
		return target{}, problem{http.StatusForbidden, noOrganization}
	case n > 1:
		return target{}, problem{http.StatusBadRequest, severalOrgs}
	}
	return target{orgID: orgID}, nil
}
