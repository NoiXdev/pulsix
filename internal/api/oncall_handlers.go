package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/oncall"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The on-call rotation API (v0.4c design doc, "API and permissions"): CRUD
// over an organization's rotations, the roster view a rotation's edit
// screen needs (GET .../shifts), and the stand-in routes nested under a
// rotation (.../exceptions) the GUI's stand-ins section reads and writes.
// Modelled on registerAlertRouteRoutes (alert_route_handlers.go) throughout
// — the same requireOrg scope resolution, the same If-Match contract on
// PATCH, the same one-mapXErr-per-package sentinel mapping.
//
// Reads use channels.read and writes use channels.write — the *channel*
// permission, not checks.read/checks.write that guard alerts. This is
// deliberate, the same reasoning alert_route_handlers.go gives its own
// routes: a rotation decides where alert data LEAVES the system (to which
// channel is on call right now), which weighs more than acknowledging an
// alert, the bar v0.4a's own design doc put at checks.write.

type oncallRotationListOutput struct {
	Body struct {
		Rotations []OncallRotationDTO `json:"rotations"`
	}
}

type oncallRotationIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type oncallRotationCreateInput struct {
	Org  string `path:"org"`
	Body OncallRotationBody
}

type oncallRotationUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The rotation's ETag; required"`
	Body    OncallRotationBody
}

type oncallRotationOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Rotation OncallRotationDTO `json:"rotation"`
	}
}

type oncallShiftsInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	From string `query:"from" required:"true" doc:"RFC 3339 start of the range"`
	To   string `query:"to" required:"true" doc:"RFC 3339 end of the range (exclusive)"`
}

type oncallShiftsOutput struct {
	Body struct {
		Shifts []OncallShiftDTO `json:"shifts"`
	}
}

type oncallExceptionListInput struct {
	Org string `path:"org"`
	ID  string `path:"id" doc:"The rotation id"`
}

type oncallExceptionCreateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id" doc:"The rotation id"`
	Body OncallExceptionBody
}

type oncallExceptionDeleteInput struct {
	Org         string `path:"org"`
	ID          string `path:"id" doc:"The rotation id"`
	ExceptionID string `path:"exceptionID"`
}

type oncallExceptionListOutput struct {
	Body struct {
		Exceptions []OncallExceptionDTO `json:"exceptions"`
	}
}

type oncallExceptionOutput struct {
	Body struct {
		Exception OncallExceptionDTO `json:"exception"`
	}
}

// registerOncallRoutes wires five rotation routes
// (list/create/get/update/delete over /orgs/{org}/oncall-rotations), the
// roster view (GET .../shifts) and the three stand-in routes nested under a
// rotation (create/list/delete over .../exceptions) — nine routes in all,
// modelled on registerAlertRouteRoutes (alert_route_handlers.go) throughout:
// the same requireOrg scope resolution, the same If-Match contract on
// PATCH, the same one-mapXErr-per-package sentinel mapping.
func registerOncallRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/oncall-rotations", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*oncallRotationListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Oncall.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		out := &oncallRotationListOutput{}
		out.Body.Rotations = make([]OncallRotationDTO, len(list))
		for i, r := range list {
			out.Body.Rotations[i] = toOncallRotationDTO(r)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/oncall-rotations", func(ctx context.Context, in *oncallRotationCreateInput) (*oncallRotationOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		input, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		r, err := deps.Oncall.Create(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "created on-call rotation "+r.Name)
		return oncallRotationOut(r), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createOncallRotation", "oncall_rotation"))

	huma.Get(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}", func(ctx context.Context, in *oncallRotationIDInput) (*oncallRotationOutput, error) {
		scope, id, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		r, err := deps.Oncall.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		return oncallRotationOut(r), nil
	})

	huma.Patch(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}", func(ctx context.Context, in *oncallRotationUpdateInput) (*oncallRotationOutput, error) {
		scope, id, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
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
		r, err := deps.Oncall.Update(ctx, scope.OrgID, id, rev, input)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "updated on-call rotation "+r.Name)
		return oncallRotationOut(r), nil
	}, withAudit("updateOncallRotation", "oncall_rotation"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}", func(ctx context.Context, in *oncallRotationIDInput) (*okOutput, error) {
		scope, id, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Oncall.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted on-call rotation")
		return &okOutput{}, nil
	}, withAudit("deleteOncallRotation", "oncall_rotation"))

	huma.Get(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}/shifts", func(ctx context.Context, in *oncallShiftsInput) (*oncallShiftsOutput, error) {
		scope, id, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		from, err := time.Parse(time.RFC3339, in.From)
		if err != nil {
			return nil, huma.Error400BadRequest("from must be an RFC 3339 time")
		}
		to, err := time.Parse(time.RFC3339, in.To)
		if err != nil {
			return nil, huma.Error400BadRequest("to must be an RFC 3339 time")
		}
		// deps.Oncall.Roster already resolves each shift's channel name
		// (RotationStore.ChannelNames, joined in the store alongside the
		// rotation's channels and exceptions — oncall.Service.Roster's own
		// doc comment) — one service call, nothing left for this handler to
		// join itself.
		shifts, err := deps.Oncall.Roster(ctx, scope.OrgID, id, from, to)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		out := &oncallShiftsOutput{}
		out.Body.Shifts = make([]OncallShiftDTO, len(shifts))
		for i, sh := range shifts {
			out.Body.Shifts[i] = toOncallShiftDTO(sh)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}/exceptions", func(ctx context.Context, in *oncallExceptionCreateInput) (*oncallExceptionOutput, error) {
		scope, rotationID, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		input, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		e, err := deps.Oncall.CreateException(ctx, scope.OrgID, rotationID, input)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		setAuditObject(ctx, e.ID.String(), "created on-call stand-in")
		out := &oncallExceptionOutput{}
		out.Body.Exception = toOncallExceptionDTO(e)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createOncallException", "oncall_exception"))

	huma.Get(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}/exceptions", func(ctx context.Context, in *oncallExceptionListInput) (*oncallExceptionListOutput, error) {
		scope, rotationID, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		// The rotation itself, first: an id from another organization must
		// read as "no such rotation" rather than confirming (via an empty
		// or populated exception list) that it exists somewhere — the same
		// ordering registerAlertRouteRoutes' GET /hosts/{id}/alert-routes
		// gives its own host lookup, and registerOncallRoutes' own
		// GET .../shifts gives through Roster's Get.
		if _, err := deps.Oncall.Get(ctx, scope.OrgID, rotationID); err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		list, err := deps.Oncall.ListExceptions(ctx, scope.OrgID, rotationID)
		if err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		out := &oncallExceptionListOutput{}
		out.Body.Exceptions = make([]OncallExceptionDTO, len(list))
		for i, e := range list {
			out.Body.Exceptions[i] = toOncallExceptionDTO(e)
		}
		return out, nil
	})

	huma.Delete(hapi, "/api/v1/orgs/{org}/oncall-rotations/{id}/exceptions/{exceptionID}", func(ctx context.Context, in *oncallExceptionDeleteInput) (*okOutput, error) {
		scope, _, err := oncallRotationScope(ctx, deps, in.Org, in.ID, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		exceptionID, err := parseUUID(in.ExceptionID, "exception id")
		if err != nil {
			return nil, err
		}
		if err := deps.Oncall.DeleteException(ctx, scope.OrgID, exceptionID); err != nil {
			return nil, mapOncallErr(ctx, err)
		}
		setAuditObject(ctx, exceptionID.String(), "deleted on-call stand-in")
		return &okOutput{}, nil
	}, withAudit("deleteOncallException", "oncall_exception"))
}

// oncallRotationScope resolves both the caller's authorization and the
// path's rotation id in one call, the same shape alertRouteScope
// (alert_route_handlers.go) gives its own resource.
func oncallRotationScope(ctx context.Context, deps Deps, org, rawID string, perm tenancy.Permission) (Scope, uuid.UUID, error) {
	scope, err := requireOrg(ctx, deps, org, perm)
	if err != nil {
		return Scope{}, uuid.Nil, err
	}
	id, err := parseUUID(rawID, "rotation id")
	return scope, id, err
}

// oncallRotationOut shapes a single rotation, ETag carrying its revision for
// a later If-Match.
func oncallRotationOut(r oncall.Rotation) *oncallRotationOutput {
	out := &oncallRotationOutput{ETag: revisionETag(r.Revision)}
	out.Body.Rotation = toOncallRotationDTO(r)
	return out
}

// mapOncallErr maps an internal/oncall error to its HTTP status, the same
// shape mapAlertRouteErr gives its own package's identically-named sentinels.
func mapOncallErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, oncall.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, oncall.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, oncall.ErrNameTaken):
		return huma.Error409Conflict("a rotation with this name already exists")
	case errors.Is(err, oncall.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	default:
		slog.ErrorContext(ctx, "unmapped oncall error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
