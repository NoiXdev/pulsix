package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgCreateInput struct {
	Body struct {
		// The pattern mirrors what the slug becomes part of: a URL path
		// segment and (from Task 9) an API token's scope. Lowercase
		// letters, digits and single hyphens between segments keep it
		// unambiguous in both places. (A struct tag must be a literal, so
		// this can't be shared as a named constant.)
		Slug string `json:"slug" minLength:"1" maxLength:"63" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$"`
		Name string `json:"name" minLength:"1"`
	}
}

// orgDTO is the wire shape of one organization, always alongside the
// caller's own role in it — there is no route that returns an org without
// also saying what the caller may do with it.
type orgDTO struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type orgOutput struct {
	Body struct {
		Org orgDTO `json:"org"`
	}
}

type orgListOutput struct {
	Body struct {
		Orgs []orgDTO `json:"orgs"`
	}
}

type orgGetInput struct {
	Org string `path:"org"`
}

type orgUpdateInput struct {
	Org  string `path:"org"`
	Body struct {
		Name string `json:"name" minLength:"1"`
	}
}

// registerOrgRoutes wires the two organization routes that are not scoped
// to an existing org and so never consult auth.TokenScopeFromContext:
// creating one (any logged-in user; the caller becomes its owner) and
// listing the caller's own. Both are registered directly on the root API,
// deliberately outside the org-scoped *huma.Group api.go mounts
// auth.BearerMiddleware on — a bearer token is meant to act within the one
// org it was minted for, not to enumerate or create organizations, so
// neither route resolves a bearer credential at all; only a cookie session
// can reach them.
func registerOrgRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs", func(ctx context.Context, in *orgCreateInput) (*orgOutput, error) {
		p, err := requireMFAComplete(ctx)
		if err != nil {
			return nil, err
		}
		org, err := deps.Tenancy.CreateOrg(ctx, p.UserID, in.Body.Slug, in.Body.Name)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		// This route never calls requireOrg (there is no existing org to
		// resolve — it is creating one), so unlike every other audited
		// handler it must fill in the audit carrier's scope itself rather
		// than getting it for free; see setAuditScope's doc comment.
		setAuditScope(ctx, &org.ID, p.UserID)
		setAuditObject(ctx, org.ID.String(), "created organization "+org.Name)
		out := &orgOutput{}
		out.Body.Org = toOrgDTO(org, tenancy.RoleOwner)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createOrganization", "organization"))

	huma.Get(hapi, "/api/v1/orgs", func(ctx context.Context, _ *struct{}) (*orgListOutput, error) {
		p, err := requireMFAComplete(ctx)
		if err != nil {
			return nil, err
		}
		memberships, err := deps.Tenancy.ListOrgsForUser(ctx, p.UserID)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		out := &orgListOutput{}
		out.Body.Orgs = make([]orgDTO, len(memberships))
		for i, m := range memberships {
			out.Body.Orgs[i] = orgDTO{ID: m.OrgID.String(), Slug: m.Slug, Name: m.Name, Role: string(m.Role), CreatedAt: m.CreatedAt}
		}
		return out, nil
	})
}

// registerOrgScopedRoutes wires the two organization routes that take an
// existing org in their path and so go through requireOrg — reading it,
// which any member may do, and renaming it, which requires PermOrgManage.
// It is mounted on the bearer-enabled *huma.Group (see api.go), since both
// consult auth.TokenScopeFromContext by way of requireOrg.
func registerOrgScopedRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}", func(ctx context.Context, in *orgGetInput) (*orgOutput, error) {
		// requireOrg already resolved the slug to look up membership and cap
		// the role; scope.Org carries that same row, so there is no need to
		// resolve it a second time here (adjacent fix, Task 10 fix round 1).
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		out := &orgOutput{}
		out.Body.Org = toOrgDTO(scope.Org, scope.Role)
		return out, nil
	})

	huma.Patch(hapi, "/api/v1/orgs/{org}", func(ctx context.Context, in *orgUpdateInput) (*orgOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgManage)
		if err != nil {
			return nil, err
		}
		org, err := deps.Tenancy.UpdateOrg(ctx, scope.OrgID, in.Body.Name)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		setAuditObject(ctx, org.ID.String(), "renamed organization to "+org.Name)
		out := &orgOutput{}
		out.Body.Org = toOrgDTO(org, scope.Role)
		return out, nil
	}, withAudit("updateOrganization", "organization"))
}

func toOrgDTO(o tenancy.Org, role tenancy.Role) orgDTO {
	return orgDTO{ID: o.ID.String(), Slug: o.Slug, Name: o.Name, Role: string(role), CreatedAt: o.CreatedAt}
}

// bearerAuthMiddleware adapts auth.BearerMiddleware — an ordinary net/http
// middleware — into the huma.Context middleware shape a *huma.Group needs.
// huma.Group.UseMiddleware wraps each operation registered on the group
// individually (see huma.NewGroup), so this scopes bearer-credential
// resolution to exactly the operations registered on that group instead of
// the whole chi router every route in this package shares — the mounting
// api.go's own doc comment on Deps describes. It reuses
// auth.BearerMiddleware verbatim (same credential parsing, same
// Tokens.Resolve call, same WithPrincipal/TokenScope wiring) rather than
// reimplementing any of it: BearerMiddleware's ctxKey/scopeKey are private
// to package auth, so there is no way to set them from here except by
// calling through the real middleware.
func bearerAuthMiddleware(tokens *auth.Tokens) func(huma.Context, func(huma.Context)) {
	mw := auth.BearerMiddleware(tokens)
	return func(ctx huma.Context, next func(huma.Context)) {
		r, w := humachi.Unwrap(ctx)
		var resolved *http.Request
		mw(http.HandlerFunc(func(_ http.ResponseWriter, rr *http.Request) {
			resolved = rr
		})).ServeHTTP(w, r)
		if resolved == nil {
			// BearerMiddleware rejected an Authorization header that named
			// itself as a bearer credential but failed to resolve: it has
			// already written the 401 response itself (see its own doc
			// comment) and never called the inner handler above, so
			// resolved stays nil and this request must stop here too,
			// rather than fall through to the operation handler.
			return
		}
		next(huma.WithContext(ctx, resolved.Context()))
	}
}
