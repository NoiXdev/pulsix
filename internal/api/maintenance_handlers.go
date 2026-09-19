package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type maintenanceWindowDTO struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	TargetKind  string     `json:"target_kind" enum:"host,inventory_group,org"`
	TargetID    string     `json:"target_id,omitempty"`
	TargetName  string     `json:"target_name,omitempty"`
	StartsAt    *time.Time `json:"starts_at,omitempty"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
	Cron        string     `json:"cron,omitempty"`
	Description string     `json:"description,omitempty" doc:"Human text for simple cron shapes"`
	Duration    string     `json:"duration,omitempty" doc:"Length of each occurrence, e.g. 2h or 90m"`
	Timezone    string     `json:"timezone,omitempty"`
	Note        string     `json:"note,omitempty"`
	ActiveNow   bool       `json:"active_now"`
	Revision    int64      `json:"revision"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// MaintenanceWindowBody is the create/update body: starts_at and ends_at
// for a one-off window, or cron, duration and timezone for a recurring one.
type MaintenanceWindowBody struct {
	Name       string     `json:"name" minLength:"1" maxLength:"100"`
	TargetKind string     `json:"target_kind" enum:"host,inventory_group,org"`
	TargetID   string     `json:"target_id,omitempty" doc:"The host or inventory group; omit for the organization"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	Cron       string     `json:"cron,omitempty" maxLength:"200"`
	Duration   string     `json:"duration,omitempty" maxLength:"20" doc:"1m to 744h, whole minutes"`
	Timezone   string     `json:"timezone,omitempty" maxLength:"64"`
	Note       string     `json:"note,omitempty" maxLength:"1000"`
}

type maintenanceListOutput struct {
	Body struct {
		Windows []maintenanceWindowDTO `json:"windows"`
	}
}

type maintenanceIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type maintenanceCreateInput struct {
	Org  string `path:"org"`
	Body MaintenanceWindowBody
}

type maintenanceUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The window's ETag; required"`
	Body    MaintenanceWindowBody
}

type maintenanceOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Window maintenanceWindowDTO `json:"window"`
	}
}

// registerMaintenanceRoutes wires spec §6's windows: maintenance.read
// (viewer) to see them, maintenance.write (operator) to plan them.
func registerMaintenanceRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/maintenance-windows", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*maintenanceListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMaintenanceRead)
		if err != nil {
			return nil, err
		}
		list, active, err := deps.Maintenance.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapMaintenanceErr(ctx, err)
		}
		out := &maintenanceListOutput{}
		out.Body.Windows = make([]maintenanceWindowDTO, len(list))
		for i, w := range list {
			out.Body.Windows[i] = toMaintenanceWindowDTO(w, active[w.ID])
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/maintenance-windows", func(ctx context.Context, in *maintenanceCreateInput) (*maintenanceOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMaintenanceWrite)
		if err != nil {
			return nil, err
		}
		input, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		w, err := deps.Maintenance.Create(ctx, scope.OrgID, scope.UserID, input)
		if err != nil {
			return nil, mapMaintenanceErr(ctx, err)
		}
		setAuditObject(ctx, w.ID.String(), "created maintenance window "+w.Name)
		return maintenanceOut(w), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createMaintenanceWindow", "maintenance"))

	huma.Get(hapi, "/api/v1/orgs/{org}/maintenance-windows/{id}", func(ctx context.Context, in *maintenanceIDInput) (*maintenanceOutput, error) {
		scope, id, err := maintenanceScope(ctx, deps, in.Org, in.ID, tenancy.PermMaintenanceRead)
		if err != nil {
			return nil, err
		}
		w, err := deps.Maintenance.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapMaintenanceErr(ctx, err)
		}
		return maintenanceOut(w), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/maintenance-windows/{id}", func(ctx context.Context, in *maintenanceUpdateInput) (*maintenanceOutput, error) {
		scope, id, err := maintenanceScope(ctx, deps, in.Org, in.ID, tenancy.PermMaintenanceWrite)
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
		w, err := deps.Maintenance.Update(ctx, scope.OrgID, id, rev, input)
		if err != nil {
			return nil, mapMaintenanceErr(ctx, err)
		}
		setAuditObject(ctx, w.ID.String(), "updated maintenance window "+w.Name)
		return maintenanceOut(w), nil
	}, withAudit("updateMaintenanceWindow", "maintenance"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/maintenance-windows/{id}", func(ctx context.Context, in *maintenanceIDInput) (*okOutput, error) {
		scope, id, err := maintenanceScope(ctx, deps, in.Org, in.ID, tenancy.PermMaintenanceWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Maintenance.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapMaintenanceErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted maintenance window")
		return &okOutput{}, nil
	}, withAudit("deleteMaintenanceWindow", "maintenance"))
}

func (b MaintenanceWindowBody) input() (maintenance.Input, error) {
	target, err := optionalUUID(b.TargetID, "target id")
	if err != nil {
		return maintenance.Input{}, err
	}
	return maintenance.Input{
		Name: b.Name, TargetKind: maintenance.TargetKind(b.TargetKind), TargetID: target,
		StartsAt: b.StartsAt, EndsAt: b.EndsAt, Cron: b.Cron, Duration: b.Duration, Timezone: b.Timezone, Note: b.Note,
	}, nil
}

func toMaintenanceWindowDTO(w maintenance.Window, active bool) maintenanceWindowDTO {
	dto := maintenanceWindowDTO{
		ID: w.ID.String(), Name: w.Name, TargetKind: string(w.TargetKind), TargetName: w.TargetName,
		StartsAt: w.StartsAt, EndsAt: w.EndsAt, Cron: w.Cron, Duration: w.DurationText(), Timezone: w.Timezone,
		Note: w.Note, ActiveNow: active, Revision: w.Revision, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	if w.TargetID != nil {
		dto.TargetID = w.TargetID.String()
	}
	if w.Cron != "" {
		dto.Description = cronexpr.Describe(w.Cron)
	}
	return dto
}

func maintenanceScope(ctx context.Context, deps Deps, org, rawID string, perm tenancy.Permission) (Scope, uuid.UUID, error) {
	scope, err := requireOrg(ctx, deps, org, perm)
	if err != nil {
		return Scope{}, uuid.Nil, err
	}
	id, err := parseUUID(rawID, "window id")
	return scope, id, err
}

// maintenanceOut shapes a single window; active_now is not computed for a
// single read (the list carries it).
func maintenanceOut(w maintenance.Window) *maintenanceOutput {
	out := &maintenanceOutput{ETag: revisionETag(w.Revision)}
	out.Body.Window = toMaintenanceWindowDTO(w, false)
	return out
}

func mapMaintenanceErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, maintenance.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, maintenance.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, maintenance.ErrNameTaken):
		return huma.Error409Conflict("a maintenance window with this name already exists")
	case errors.Is(err, maintenance.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	default:
		slog.ErrorContext(ctx, "unmapped maintenance error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
