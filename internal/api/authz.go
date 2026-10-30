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
// request, resolved by requireOrg. Org carries the already-resolved
// organization row itself, not just its id, so a handler that needs the
// org's other fields (e.g. GET /orgs/{org} returning its name) never has to
// resolve the same slug a second time (adjacent fix, Task 10 fix round 1).
type Scope struct {
	OrgID  uuid.UUID
	UserID uuid.UUID
	Role   tenancy.Role
	Org    tenancy.Org
}

// requireOrg resolves the org named by orgSlug and checks that the calling
// principal may perform perm against it. It is the single authorization
// gate every org-scoped route (organizations, members, invites, tokens)
// goes through, and it fails closed at every step, in this order:
//
//  0. The request did not pass through orgScopeMiddleware, so its database
//     scope is not bound to this organization → 500 (a wiring bug, never
//     served as an unscoped request).
//  1. No principal at all → 401.
//  2. A session principal that has not completed its second factor → 403
//     (the same rule requireMFAComplete applies everywhere else; a bearer
//     token principal is always MFA-complete by construction, see
//     auth.Tokens.Resolve, so this only ever rejects a pending session).
//  3. The org does not exist → 404.
//  4. The principal is not a member of the org → 403. This is deliberately
//     distinguished from step 3's 404 (an unknown slug and "you're a member
//     of an org that exists but not this one" read differently), following
//     the brief's own step order — an org slug is not treated as a secret
//     an outsider must be kept from confirming exists.
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
	// The organization was already resolved, and the request's database
	// scope bound to it, by orgScopeMiddleware; requireOrg authorizes
	// against that same resolution rather than looking the slug up a second
	// time, and refuses to run at all on a request that never passed through
	// it (see orgRouteFromContext).
	route, err := orgRouteFromContext(ctx, orgSlug)
	if err != nil {
		return Scope{}, err
	}
	p, ok := auth.FromContext(ctx)
	if !ok {
		return Scope{}, huma.Error401Unauthorized("login required")
	}
	if !p.MFAComplete {
		return Scope{}, huma.Error403Forbidden("second factor required")
	}
	if route.err != nil {
		return Scope{}, mapTenancyErr(ctx, route.err)
	}
	org := route.org
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
	// Fills in the request's audit carrier, if it has one (only operations
	// registered with withAudit do — see audit_middleware.go). Every
	// org-scoped mutating handler calls requireOrg to authorize itself in
	// the first place, so wiring this in here, rather than asking each
	// handler to call setAuditScope separately, is what makes the org and
	// actor on a recorded event impossible to get wrong or forget: there is
	// no separate step to skip.
	setAuditScope(ctx, &org.ID, p.UserID)
	return Scope{OrgID: org.ID, UserID: p.UserID, Role: role, Org: org}, nil
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
	case errors.Is(err, tenancy.ErrLastOwner), errors.Is(err, tenancy.ErrLastInstanceAdmin):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, tenancy.ErrSlugTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, tenancy.ErrSignInRequired):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, tenancy.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, tenancy.ErrInviteDelivery):
		// The SMTP server's own answer can name internal hosts and
		// credentials, and the caller is an organization administrator,
		// not necessarily the instance administrator who owns those
		// settings: it is logged, and the client gets a specific but
		// generic explanation. The invitation itself was not kept.
		slog.ErrorContext(ctx, "invitation e-mail could not be delivered", "err", err)
		return huma.Error502BadGateway("the invitation e-mail could not be delivered, so no invitation was created; check the instance's outgoing mail settings and try again")
	default:
		slog.ErrorContext(ctx, "unmapped tenancy error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
