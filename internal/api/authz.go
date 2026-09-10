package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// Scope is the authorized (org, user, effective role) for one org-scoped
// request, resolved by requireOrg.
type Scope struct {
	OrgID  uuid.UUID
	UserID uuid.UUID
	Role   tenancy.Role
}

// requireOrg resolves the org named by orgSlug and checks that the calling
// principal may perform perm against it. It is the single authorization
// gate every org-scoped route (organizations, members, invites, tokens)
// goes through, and it fails closed at every step, in this order:
//
//  1. No principal at all → 401.
//  2. A session principal that has not completed its second factor → 403
//     (the same rule requireMFAComplete applies everywhere else; a bearer
//     token principal is always MFA-complete by construction, see
//     auth.Tokens.Resolve, so this only ever rejects a pending session).
//  3. The org does not exist → 404 (never distinguished from "exists but
//     you're not authorized to know that" — see mapTenancyErr).
//  4. The principal is not a member of the org → 403.
//  5. A bearer token is present (auth.TokenScopeFromContext) and its scope
//     names a *different* org than the one being accessed → 403. A token
//     can never be used to reach an org it wasn't minted for, regardless of
//     the caller's membership elsewhere.
//  6. The effective role is the *lower* of the caller's membership role and
//     the token's own role (tenancy.Lower) — a token can only ever narrow
//     what its owner could otherwise do, never widen it. For a cookie
//     session (no TokenScope), the effective role is just the membership
//     role.
//  7. tenancy.Allows(effective role, perm) — false → 403.
func requireOrg(ctx context.Context, deps Deps, orgSlug string, perm tenancy.Permission) (Scope, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return Scope{}, huma.Error401Unauthorized("login required")
	}
	if !p.MFAComplete {
		return Scope{}, huma.Error403Forbidden("second factor required")
	}
	org, err := deps.Tenancy.GetOrgBySlug(ctx, orgSlug)
	if err != nil {
		return Scope{}, mapTenancyErr(ctx, err)
	}
	role, err := deps.Tenancy.MembershipOf(ctx, org.ID, p.UserID)
	if err != nil {
		return Scope{}, mapTenancyErr(ctx, err)
	}
	if ts, ok := auth.TokenScopeFromContext(ctx); ok {
		if ts.OrgID != org.ID {
			return Scope{}, huma.Error403Forbidden("token is scoped to another organization")
		}
		role = tenancy.Lower(role, tenancy.Role(ts.Role))
	}
	if !tenancy.Allows(role, perm) {
		return Scope{}, huma.Error403Forbidden("insufficient role")
	}
	return Scope{OrgID: org.ID, UserID: p.UserID, Role: role}, nil
}

// requireInstanceAdmin gates instance-level routes that are not scoped to
// any organization (POST /admin/users/{id}/deactivate). It builds on
// requireInteractiveSession, not the bare requireSession the brief for this
// task sketched: deactivating a user revokes their sessions and tokens
// outright (tenancy.Service.DeactivateUser), which is exactly the kind of
// account-security-impacting action requireInteractiveSession already
// exists to gate everywhere else — a half-authenticated session must not
// reach it, and neither should a bearer token, however privileged its
// owner, since there is no per-org scope here for a token to be narrowed
// against.
func requireInstanceAdmin(ctx context.Context) (auth.Principal, error) {
	p, err := requireInteractiveSession(ctx)
	if err != nil {
		return p, err
	}
	if !p.InstanceAdmin {
		return p, huma.Error403Forbidden("instance admin required")
	}
	return p, nil
}

// mapTenancyErr converts tenancy.Service sentinels to HTTP errors, the same
// shape mapAuthErr and mapTokenErr give their own packages' sentinels:
// anything not recognized is logged server-side and replaced with a generic
// 500 rather than ever reaching the client.
func mapTenancyErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, tenancy.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, tenancy.ErrNotMember):
		return huma.Error403Forbidden("not a member of this organization")
	case errors.Is(err, tenancy.ErrForbidden):
		return huma.Error403Forbidden(err.Error())
	case errors.Is(err, tenancy.ErrLastOwner):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, tenancy.ErrSlugTaken):
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped tenancy error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
