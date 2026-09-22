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
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type scheduleListInput struct {
	Org        string `path:"org"`
	TemplateID string `query:"template_id" doc:"Only this template's schedules"`
}

type scheduleListOutput struct {
	Body struct {
		Schedules []scheduleDTO `json:"schedules"`
	}
}

type scheduleIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type scheduleCreateInput struct {
	Org  string `path:"org"`
	Body ScheduleBody
}

type scheduleUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The schedule's ETag; required"`
	Body    ScheduleBody
}

type scheduleOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Schedule scheduleDTO `json:"schedule"`
	}
}

type schedulePreviewInput struct {
	Org  string `path:"org"`
	Body struct {
		Mode     string     `json:"mode" enum:"cron,simple"`
		Cron     string     `json:"cron,omitempty" maxLength:"200"`
		Simple   *simpleDTO `json:"simple,omitempty"`
		Timezone string     `json:"timezone" minLength:"1" maxLength:"64"`
	}
}

type schedulePreviewOutput struct {
	Body struct {
		Cron        string      `json:"cron"`
		Description string      `json:"description"`
		Next        []time.Time `json:"next"`
	}
}

type schedulePauseAllOutput struct {
	Body struct {
		Paused int `json:"paused"`
	}
}

// listLimitMin, listLimitMax and listLimitDefault bound every "recent
// items, newest first" list page this package hands a caller-supplied
// limit to: the schedule runs history (1-100, default 50) and the
// notification channel delivery log (notify_handlers.go), both to the
// same range. The query tag on each input struct already rejects an
// out-of-range value before its handler runs, but clampListLimit is the
// defense a value reaching the backing service by any other path still
// needs — schedules.Service.Runs and notify.Service.Deliveries both pass
// limit straight to Postgres, and a negative one is a database error, not
// a validation error (Task 5 deferred minor; generalized from
// clampScheduleRunsLimit in Task 4 fix round 1 once notify_handlers.go
// became a second caller).
const (
	listLimitMin     = 1
	listLimitMax     = 100
	listLimitDefault = 50
)

func clampListLimit(limit int) int {
	switch {
	case limit <= 0:
		return listLimitDefault
	case limit > listLimitMax:
		return listLimitMax
	case limit < listLimitMin:
		return listLimitMin
	default:
		return limit
	}
}

type scheduleRunsInput struct {
	Org   string `path:"org"`
	ID    string `path:"id"`
	Limit int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
}

type scheduleRunsOutput struct {
	Body struct {
		Runs []scheduleRunDTO `json:"runs"`
	}
}

// registerScheduleRoutes wires spec §2's schedules: templates.read to see,
// templates.write to manage, plus jobs.run for "run now".
func registerScheduleRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/schedules", func(ctx context.Context, in *scheduleListInput) (*scheduleListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		tmplID, err := optionalUUID(in.TemplateID, "template id")
		if err != nil {
			return nil, err
		}
		list, latest, err := deps.Schedules.List(ctx, scope.OrgID, tmplID)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		out := &scheduleListOutput{}
		out.Body.Schedules = make([]scheduleDTO, len(list))
		for i, s := range list {
			var last *schedules.Run
			if r, ok := latest[s.ID]; ok {
				last = &r
			}
			out.Body.Schedules[i] = toScheduleDTO(s, last)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/schedules", func(ctx context.Context, in *scheduleCreateInput) (*scheduleOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		input, err := scheduleInput(in.Body)
		if err != nil {
			return nil, err
		}
		s, err := deps.Schedules.Create(ctx, scope.OrgID, scope.UserID, input)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		setAuditObject(ctx, s.ID.String(), "created schedule "+s.Name)
		return scheduleOut(s), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createSchedule", "schedule"))

	huma.Post(hapi, "/api/v1/orgs/{org}/schedules/preview", func(ctx context.Context, in *schedulePreviewInput) (*schedulePreviewOutput, error) {
		if _, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesRead); err != nil {
			return nil, err
		}
		var simple *cronexpr.Simple
		if in.Body.Simple != nil {
			simple = &cronexpr.Simple{Kind: in.Body.Simple.Kind, Every: in.Body.Simple.Every, Time: in.Body.Simple.Time, Days: in.Body.Simple.Days, Day: in.Body.Simple.Day}
		}
		p, err := deps.Schedules.Preview(schedules.PreviewInput{Mode: schedules.Mode(in.Body.Mode), Cron: in.Body.Cron, Simple: simple, Timezone: in.Body.Timezone})
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		out := &schedulePreviewOutput{}
		out.Body.Cron, out.Body.Description, out.Body.Next = p.Cron, p.Description, p.Next
		return out, nil
	}, func(o *huma.Operation) { o.OperationID = "previewSchedule" })

	huma.Post(hapi, "/api/v1/orgs/{org}/schedules/pause-all", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*schedulePauseAllOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		n, err := deps.Schedules.PauseAll(ctx, scope.OrgID)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		setAuditObject(ctx, "", "paused all schedules")
		out := &schedulePauseAllOutput{}
		out.Body.Paused = n
		return out, nil
	}, withAudit("pauseAllSchedules", "schedule"))

	huma.Get(hapi, "/api/v1/orgs/{org}/schedules/{id}", func(ctx context.Context, in *scheduleIDInput) (*scheduleOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		s, err := deps.Schedules.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		return scheduleOut(s), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/schedules/{id}", func(ctx context.Context, in *scheduleUpdateInput) (*scheduleOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		input, err := scheduleInput(in.Body)
		if err != nil {
			return nil, err
		}
		s, err := deps.Schedules.Update(ctx, scope.OrgID, id, rev, input)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		setAuditObject(ctx, s.ID.String(), "updated schedule "+s.Name)
		return scheduleOut(s), nil
	}, withAudit("updateSchedule", "schedule"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/schedules/{id}", func(ctx context.Context, in *scheduleIDInput) (*okOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Schedules.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted schedule")
		return &okOutput{}, nil
	}, withAudit("deleteSchedule", "schedule"))

	for _, action := range []struct {
		path, op, summary string
		pause             bool
	}{
		{"/pause", "pauseSchedule", "paused schedule ", true},
		{"/resume", "resumeSchedule", "resumed schedule ", false},
	} {
		huma.Post(hapi, "/api/v1/orgs/{org}/schedules/{id}"+action.path, func(ctx context.Context, in *scheduleIDInput) (*scheduleOutput, error) {
			scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesWrite)
			if err != nil {
				return nil, err
			}
			var s schedules.Schedule
			if action.pause {
				s, err = deps.Schedules.Pause(ctx, scope.OrgID, id)
			} else {
				s, err = deps.Schedules.Resume(ctx, scope.OrgID, id)
			}
			if err != nil {
				return nil, mapSchedulesErr(ctx, err)
			}
			setAuditObject(ctx, s.ID.String(), action.summary+s.Name)
			return scheduleOut(s), nil
		}, withAudit(action.op, "schedule"))
	}

	huma.Post(hapi, "/api/v1/orgs/{org}/schedules/{id}/run", func(ctx context.Context, in *scheduleIDInput) (*jobStartOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		if !tenancy.Allows(scope.Role, tenancy.PermJobsRun) {
			return nil, huma.Error403Forbidden("insufficient role")
		}
		created, err := deps.Schedules.RunNow(ctx, scope.OrgID, id, scope.UserID)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "ran schedule now")
		return jobStart(created), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("runSchedule", "schedule"))

	huma.Get(hapi, "/api/v1/orgs/{org}/schedules/{id}/runs", func(ctx context.Context, in *scheduleRunsInput) (*scheduleRunsOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		runs, err := deps.Schedules.Runs(ctx, scope.OrgID, id, clampListLimit(in.Limit))
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		out := &scheduleRunsOutput{}
		out.Body.Runs = make([]scheduleRunDTO, len(runs))
		for i, r := range runs {
			out.Body.Runs[i] = toScheduleRunDTO(r)
		}
		return out, nil
	})
}

func scheduleScope(ctx context.Context, deps Deps, org, rawID string, perm tenancy.Permission) (Scope, uuid.UUID, error) {
	scope, err := requireOrg(ctx, deps, org, perm)
	if err != nil {
		return Scope{}, uuid.Nil, err
	}
	id, err := parseUUID(rawID, "schedule id")
	return scope, id, err
}

func scheduleInput(b ScheduleBody) (schedules.Input, error) {
	tmplID, err := parseUUID(b.TemplateID, "template id")
	if err != nil {
		return schedules.Input{}, err
	}
	return schedules.Input{
		TemplateID: tmplID, Name: b.Name, Mode: schedules.Mode(b.Mode), Cron: b.Cron, Simple: b.simple(),
		Timezone: b.Timezone, Guards: b.guards(),
		Overrides: templates.Overrides{Limit: b.Limit, ExtraVars: b.ExtraVars},
	}, nil
}

func scheduleOut(s schedules.Schedule) *scheduleOutput {
	out := &scheduleOutput{ETag: revisionETag(s.Revision)}
	out.Body.Schedule = toScheduleDTO(s, nil)
	return out
}

func mapSchedulesErr(ctx context.Context, err error) error {
	var ve *schema.ValidationError
	switch {
	case errors.As(err, &ve):
		return schemaProblems(ve, "body.extra_vars")
	case errors.Is(err, schedules.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, schedules.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, schedules.ErrNameTaken):
		return huma.Error409Conflict("a schedule with this name already exists")
	case errors.Is(err, schedules.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	case errors.Is(err, jobs.ErrEmptyScope):
		return huma.Error422UnprocessableEntity("the template's scope resolves to no host")
	case errors.Is(err, jobs.ErrHostKeyChanged):
		return huma.Error409Conflict("every targeted host has a changed host key; re-test and re-pin first")
	case errors.Is(err, jobs.ErrUnpinnedHosts):
		// The organization requires pinned hosts (backlog 2.44): the
		// schedule's run now is refused, naming the hosts without a full
		// pin. 422: the org's requirement is not satisfied by this run.
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped schedules error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
