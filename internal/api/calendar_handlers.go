package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/calendar"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type calendarEntryDTO struct {
	ScheduleID   string    `json:"schedule_id"`
	ScheduleName string    `json:"schedule_name"`
	TemplateID   string    `json:"template_id"`
	At           time.Time `json:"at"`
	State        string    `json:"state" enum:"planned,triggered,skipped_overlap,skipped_maintenance,missed,failed"`
	Reason       string    `json:"reason,omitempty"`
	JobGroupID   string    `json:"job_group_id,omitempty"`
	JobID        string    `json:"job_id,omitempty" doc:"The run's first job, for the run view"`
	JobStatus    string    `json:"job_status,omitempty" enum:"queued,running,succeeded,failed,cancelled"`
}

type calendarWindowDTO struct {
	WindowID   string    `json:"window_id"`
	Name       string    `json:"name"`
	TargetKind string    `json:"target_kind" enum:"host,inventory_group,org"`
	TargetID   string    `json:"target_id,omitempty"`
	TargetName string    `json:"target_name,omitempty"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
}

type calendarInput struct {
	Org  string `path:"org"`
	From string `query:"from" required:"true" doc:"RFC 3339 start of the range"`
	To   string `query:"to" required:"true" doc:"RFC 3339 end of the range (exclusive), at most 6 weeks after from"`
}

type calendarOutput struct {
	Body struct {
		From        time.Time           `json:"from"`
		To          time.Time           `json:"to"`
		Entries     []calendarEntryDTO  `json:"entries"`
		Maintenance []calendarWindowDTO `json:"maintenance"`
		Truncated   bool                `json:"truncated" doc:"Some frequent schedules' occurrences were left out"`
	}
}

// registerCalendarRoutes wires spec §8's calendar: templates.read, like
// the schedules it shows.
func registerCalendarRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/calendar", func(ctx context.Context, in *calendarInput) (*calendarOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesRead)
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
		v, err := deps.Calendar.Range(ctx, scope.OrgID, from, to)
		if errors.Is(err, calendar.ErrInvalidRange) {
			return nil, huma.Error400BadRequest(err.Error())
		}
		if err != nil {
			slog.ErrorContext(ctx, "calendar", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		out := &calendarOutput{}
		out.Body.From, out.Body.To, out.Body.Truncated = v.From, v.To, v.Truncated
		out.Body.Entries = make([]calendarEntryDTO, len(v.Entries))
		for i, e := range v.Entries {
			dto := calendarEntryDTO{
				ScheduleID: e.ScheduleID.String(), ScheduleName: e.ScheduleName, TemplateID: e.TemplateID.String(),
				At: e.At, State: e.State, Reason: e.Reason, JobStatus: e.JobStatus,
			}
			if e.JobGroupID != nil {
				dto.JobGroupID = e.JobGroupID.String()
			}
			if e.JobID != nil {
				dto.JobID = e.JobID.String()
			}
			out.Body.Entries[i] = dto
		}
		out.Body.Maintenance = make([]calendarWindowDTO, len(v.Maintenance))
		for i, w := range v.Maintenance {
			dto := calendarWindowDTO{WindowID: w.WindowID.String(), Name: w.Name, TargetKind: string(w.TargetKind), TargetName: w.TargetName, Start: w.Start, End: w.End}
			if w.TargetID != nil {
				dto.TargetID = w.TargetID.String()
			}
			out.Body.Maintenance[i] = dto
		}
		return out, nil
	})
}
