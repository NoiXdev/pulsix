package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The alert-route API (v0.4b design doc, "API and permissions"): CRUD over
// an organization's routes, plus the per-host union view backlog 4.1b asks
// for (Service.ForHost's own doc comment: "why did no mail arrive?", "why
// did three?"). Six routes in all.
//
// Reads use channels.read and writes use channels.write — the *channel*
// permission, not checks.read/checks.write that guard alerts themselves.
// This is deliberate, not an oversight: a route decides where alert data
// LEAVES the system — to which address, to which webhook — which weighs
// more than acknowledging an alert, the bar v0.4a's own design doc put at
// checks.write. Being allowed to acknowledge an alert does not carry the
// right to decide where it is reported.

type alertRouteListOutput struct {
	Body struct {
		Routes []AlertRouteDTO `json:"routes"`
	}
}

type alertRouteIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type alertRouteCreateInput struct {
	Org  string `path:"org"`
	Body AlertRouteBody
}

type alertRouteUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The route's ETag; required"`
	Body    AlertRouteBody
}

type alertRouteOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Route AlertRouteDTO `json:"route"`
	}
}

type hostAlertRoutesInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// registerAlertRouteRoutes wires six routes: list/create/get/update/delete
// over /orgs/{org}/alert-routes, and the per-host union view,
// GET /orgs/{org}/hosts/{id}/alert-routes.
func registerAlertRouteRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/alert-routes", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*alertRouteListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.AlertRoutes.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapAlertRouteErr(ctx, err)
		}
		out := &alertRouteListOutput{}
		out.Body.Routes = make([]AlertRouteDTO, len(list))
		for i, r := range list {
			out.Body.Routes[i] = toAlertRouteDTO(r)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/alert-routes", func(ctx context.Context, in *alertRouteCreateInput) (*alertRouteOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		input, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		r, err := deps.AlertRoutes.Create(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapAlertRouteErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "created alert route "+r.Name)
		return alertRouteOut(r), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createAlertRoute", "alert_route"))

	huma.Get(hapi, "/api/v1/orgs/{org}/alert-routes/{id}", func(ctx context.Context, in *alertRouteIDInput) (*alertRouteOutput, error) {
		scope, id, err := alertRouteScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		r, err := deps.AlertRoutes.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapAlertRouteErr(ctx, err)
		}
		return alertRouteOut(r), nil
	})

	huma.Patch(hapi, "/api/v1/orgs/{org}/alert-routes/{id}", func(ctx context.Context, in *alertRouteUpdateInput) (*alertRouteOutput, error) {
		scope, id, err := alertRouteScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		input, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		r, err := deps.AlertRoutes.Update(ctx, scope.OrgID, id, rev, input)
		if err != nil {
			return nil, mapAlertRouteErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "updated alert route "+r.Name)
		return alertRouteOut(r), nil
	}, withAudit("updateAlertRoute", "alert_route"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/alert-routes/{id}", func(ctx context.Context, in *alertRouteIDInput) (*okOutput, error) {
		scope, id, err := alertRouteScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.AlertRoutes.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapAlertRouteErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted alert route")
		return &okOutput{}, nil
	}, withAudit("deleteAlertRoute", "alert_route"))

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/alert-routes", func(ctx context.Context, in *hostAlertRoutesInput) (*alertRouteListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// The host itself, first: an id from another organization must read
		// as "no such host" rather than confirming (via an empty or
		// populated route list) that it exists somewhere — the same
		// ordering registerHostCheckRoutes gives GET /hosts/{id}/checks.
		if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		list, err := deps.AlertRoutes.ForHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapAlertRouteErr(ctx, err)
		}
		out := &alertRouteListOutput{}
		out.Body.Routes = make([]AlertRouteDTO, len(list))
		for i, r := range list {
			out.Body.Routes[i] = toAlertRouteDTO(r)
		}
		return out, nil
	})
}

// alertRouteScope resolves both the caller's authorization and the path's
// route id in one call, the same shape alertScope (alert_handlers.go) and
// maintenanceScope (maintenance_handlers.go) each give their own resource.
func alertRouteScope(ctx context.Context, deps Deps, org, rawID string, perm tenancy.Permission) (Scope, uuid.UUID, error) {
	scope, err := requireOrg(ctx, deps, org, perm)
	if err != nil {
		return Scope{}, uuid.Nil, err
	}
	id, err := parseUUID(rawID, "alert route id")
	return scope, id, err
}

// alertRouteOut shapes a single route, ETag carrying its revision for a
// later If-Match.
func alertRouteOut(r alertroutes.Route) *alertRouteOutput {
	out := &alertRouteOutput{ETag: revisionETag(r.Revision)}
	out.Body.Route = toAlertRouteDTO(r)
	return out
}

// mapAlertRouteErr maps an internal/alertroutes error to its HTTP status,
// the same shape mapMaintenanceErr and mapNotifyErr each give their own
// domain's identically-named sentinels.
func mapAlertRouteErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, alertroutes.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, alertroutes.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, alertroutes.ErrNameTaken):
		return huma.Error409Conflict("a route with this name already exists")
	case errors.Is(err, alertroutes.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	default:
		slog.ErrorContext(ctx, "unmapped alert route error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
