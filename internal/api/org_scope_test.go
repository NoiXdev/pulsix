package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestRequireOrgFailsClosedWithoutOrgScope covers the wiring guard
// requireOrg now starts with: an org-scoped handler reached by a request
// that never passed through orgScopeMiddleware — or whose context no longer
// carries the matching database scope — is refused with 500, never served
// as an unscoped request. Each case would otherwise run its statements
// under the bypass policy.
func TestRequireOrgFailsClosedWithoutOrgScope(t *testing.T) {
	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme"}
	principal := ctxWithPrincipal(uuid.Must(uuid.NewV7()), true)
	cases := map[string]context.Context{
		"no org-scope middleware": principal,
		"slug differs from the bound one": context.WithValue(db.WithOrgScope(principal, org.ID), orgRouteKey{},
			&orgRoute{slug: "other", org: org}),
		"database scope missing": context.WithValue(principal, orgRouteKey{}, &orgRoute{slug: "acme", org: org}),
		"database scope bound to another organization": context.WithValue(db.WithOrgScope(principal, uuid.Must(uuid.NewV7())),
			orgRouteKey{}, &orgRoute{slug: "acme", org: org}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := requireOrg(ctx, Deps{}, "acme", tenancy.PermHostsRead)
			var se huma.StatusError
			if !errors.As(err, &se) || se.GetStatus() != http.StatusInternalServerError {
				t.Fatalf("err = %v, want a 500", err)
			}
		})
	}
}

// TestRequireOrgMapsAnUnresolvableSlug covers the resolution error path: a
// slug orgScopeMiddleware could not resolve is bound to no organization and
// answered with the tenancy error's own status (404 for an unknown slug),
// after the principal checks.
func TestRequireOrgMapsAnUnresolvableSlug(t *testing.T) {
	ctx := context.WithValue(db.WithOrgScope(ctxWithPrincipal(uuid.Must(uuid.NewV7()), true), uuid.Nil),
		orgRouteKey{}, &orgRoute{slug: "ghost", err: tenancy.ErrNotFound})
	_, err := requireOrg(ctx, Deps{}, "ghost", tenancy.PermHostsRead)
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404", err)
	}
}
