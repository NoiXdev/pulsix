package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// orgRouteKey is the context key orgScopeMiddleware hands its resolution of
// the path's {org} to requireOrg under.
type orgRouteKey struct{}

// orgRoute is orgScopeMiddleware's resolution of one request's {org} path
// parameter: the slug as it appeared in the path, and either the
// organization it names or the error looking it up produced.
type orgRoute struct {
	slug string
	org  tenancy.Org
	err  error
}

// orgScopeMiddleware binds every operation on the org-scoped group to the
// organization its {org} path parameter names, before the handler runs:
// db.WithOrgScope makes every statement the request issues through a store
// run inside a transaction whose pulsix.org_id is that organization, so the
// row-level-security policy on every org-owned table (memberships,
// invitations, API tokens, audit events, zones, hosts, host tags) admits
// only its rows — whatever the query text says. Tenant isolation therefore
// no longer rests on every hand-written WHERE org_id clause alone.
//
// Binding the scope is not authorization, and does not need to be: it only
// ever narrows what the database will show. requireOrg still decides whether
// the caller may act on this organization at all, and it consumes the very
// organization resolved here (see orgRouteFromContext), so the scope the
// database enforces and the organization the handler was authorized for can
// never be two different ones. A slug that names no organization is bound
// to uuid.Nil — every org-owned table then looks empty — and requireOrg
// answers 404 as before.
//
// It must run after bearerAuthMiddleware on the group: resolving a
// presented token by its hash is instance-level (the token names its own
// organization), and inside this scope a token minted for another
// organization would be invisible, turning requireOrg's precise "token is
// scoped to another organization" 403 into an indistinct 401.
func orgScopeMiddleware(deps Deps) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		ctx := hctx.Context()
		route := &orgRoute{slug: hctx.Param("org")}
		scope := uuid.Nil
		route.org, route.err = deps.Tenancy.GetOrgBySlug(ctx, route.slug)
		if route.err == nil {
			scope = route.org.ID
		}
		ctx = db.WithOrgScope(ctx, scope)
		ctx = context.WithValue(ctx, orgRouteKey{}, route)
		next(huma.WithContext(hctx, ctx))
	}
}

// orgRouteFromContext returns orgScopeMiddleware's resolution for slug. It
// fails closed — a logged 500, never a silently unscoped request — when the
// request did not pass through orgScopeMiddleware (an org-scoped handler
// registered outside the org-scoped group), when the slug the handler asks
// about is not the one the middleware bound, or when the context no longer
// carries the matching database scope.
func orgRouteFromContext(ctx context.Context, slug string) (*orgRoute, error) {
	route, ok := ctx.Value(orgRouteKey{}).(*orgRoute)
	if !ok || route.slug != slug {
		slog.ErrorContext(ctx, "org-scoped handler reached without its organization bound; is it registered on the org-scoped group?", "org", slug)
		return nil, huma.Error500InternalServerError("internal error")
	}
	if route.err == nil {
		if scope, ok := db.OrgScope(ctx); !ok || scope != route.org.ID {
			slog.ErrorContext(ctx, "org-scoped request lost its database scope", "org", slug)
			return nil, huma.Error500InternalServerError("internal error")
		}
	}
	return route, nil
}
