package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/mcp"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// mcpAccess is mcp.OrgAccess over the same resolution requireOrg uses, so
// the assistant endpoint decides an organization and a token's effective
// role there exactly as the REST API does (spec 2026-11-17 personal tokens,
// rules 2 and 7): the membership lowered by the token (tokenRole), the
// grant, the per-person block and the organization's personal token switch.
type mcpAccess struct{ deps Deps }

var _ mcp.OrgAccess = mcpAccess{}

// OrgBySlug resolves the organization a path names, as orgScopeMiddleware does.
func (a mcpAccess) OrgBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	org, err := a.deps.Tenancy.GetOrgBySlug(ctx, slug)
	if err != nil {
		return uuid.Nil, err
	}
	return org.ID, nil
}

// SingleGrant counts the token's live grants. ctx must carry no
// organization: the count fails closed under one.
func (a mcpAccess) SingleGrant(ctx context.Context, tokenID uuid.UUID) (uuid.UUID, int, error) {
	return a.deps.PersonalTokens.GrantCount(ctx, tokenID)
}

// EffectiveRole is requireOrg's role decision without a permission: the
// caller's current membership in orgID narrowed by the token. ctx is bound
// to orgID.
func (a mcpAccess) EffectiveRole(ctx context.Context, p auth.Principal, ts auth.TokenScope, orgID uuid.UUID) (tenancy.Role, error) {
	if p.TokenID == nil {
		return "", errors.New("token scope without a token id")
	}
	member, err := a.deps.Tenancy.MembershipOf(ctx, orgID, p.UserID)
	if errors.Is(err, tenancy.ErrNotMember) {
		return "", mcp.Refusal{Detail: "not a member of this organization"}
	}
	if err != nil {
		return "", fmt.Errorf("membership: %w", err)
	}
	role, err := tokenRole(ctx, a.deps, ts, *p.TokenID, orgID, member)
	if detail, refused := isTokenRefusal(err); refused {
		return "", mcp.Refusal{Detail: detail}
	}
	return role, err
}
