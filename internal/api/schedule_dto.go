package api

import (
	"time"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/schedules"
)

type simpleDTO struct {
	Kind  string   `json:"kind" enum:"every_minutes,every_hours,daily,weekly,monthly"`
	Every int      `json:"every,omitempty"`
	Time  string   `json:"time,omitempty" doc:"HH:MM in the schedule's time zone"`
	Days  []string `json:"days,omitempty" doc:"weekly: mon..sun"`
	Day   int      `json:"day,omitempty" doc:"monthly: 1-31; months without that day are skipped"`
}

type catchUpDTO struct {
	Enabled bool   `json:"enabled"`
	Window  string `json:"window,omitempty" doc:"Go duration, 1m to 24h (default 1h)"`
}

type guardsDTO struct {
	Overlap     string     `json:"overlap,omitempty" enum:"skip,queue" doc:"skip (default) records skipped_overlap while a run of the template is active"`
	CatchUp     catchUpDTO `json:"catch_up,omitempty"`
	Maintenance string     `json:"maintenance,omitempty" enum:"ignore,skip,exclude_hosts" doc:"ignore (default) runs inside maintenance windows; skip records skipped_maintenance when a targeted host is in one; exclude_hosts leaves those hosts out"`
}

type scheduleRunDTO struct {
	ID         string    `json:"id"`
	DueAt      time.Time `json:"due_at"`
	DecidedAt  time.Time `json:"decided_at"`
	Outcome    string    `json:"outcome" enum:"triggered,skipped_overlap,skipped_maintenance,missed,failed"`
	Reason     string    `json:"reason,omitempty"`
	JobGroupID string    `json:"job_group_id,omitempty"`
}

type scheduleDTO struct {
	ID          string          `json:"id"`
	TemplateID  string          `json:"template_id"`
	Name        string          `json:"name"`
	Mode        string          `json:"mode" enum:"cron,simple"`
	Cron        string          `json:"cron"`
	Description string          `json:"description" doc:"Human text for simple shapes; empty otherwise"`
	Simple      *simpleDTO      `json:"simple,omitempty"`
	Timezone    string          `json:"timezone"`
	Guards      guardsDTO       `json:"guards"`
	Limit       *string         `json:"limit,omitempty"`
	ExtraVars   map[string]any  `json:"extra_vars,omitempty" doc:"Overrides; secret values read as {\"$secret\":\"set\"}"`
	Paused      bool            `json:"paused"`
	NextRunAt   *time.Time      `json:"next_run_at,omitempty"`
	LastRunAt   *time.Time      `json:"last_run_at,omitempty"`
	LastRun     *scheduleRunDTO `json:"last_run,omitempty"`
	// NotifyOn lists the distinct categories (failure, change, success,
	// skipped) any of the schedule's notify rules cover, in canonical
	// order; empty when it has none. The schedules list's "Notify" column
	// (backlog 2.73) reads this instead of a per-row
	// GET .../notifications call.
	NotifyOn  []string  `json:"notify_on,omitempty"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScheduleBody is the create/update body (exported so huma flattens it,
// like TemplateBody).
type ScheduleBody struct {
	TemplateID string         `json:"template_id" format:"uuid"`
	Name       string         `json:"name" minLength:"1" maxLength:"100"`
	Mode       string         `json:"mode" enum:"cron,simple"`
	Cron       string         `json:"cron,omitempty" maxLength:"200"`
	Simple     *simpleDTO     `json:"simple,omitempty"`
	Timezone   string         `json:"timezone" minLength:"1" maxLength:"64"`
	Guards     *guardsDTO     `json:"guards,omitempty"`
	Limit      *string        `json:"limit,omitempty" maxLength:"500"`
	ExtraVars  map[string]any `json:"extra_vars,omitempty"`
}

func toScheduleDTO(s schedules.Schedule, last *schedules.Run, notifyOn []notify.Category) scheduleDTO {
	dto := scheduleDTO{
		ID: s.ID.String(), TemplateID: s.TemplateID.String(), Name: s.Name, Mode: string(s.Mode),
		Cron: s.Cron, Description: cronexpr.Describe(s.Cron), Timezone: s.Timezone,
		Guards: guardsDTO{Overlap: string(s.Guards.Overlap), CatchUp: catchUpDTO{Enabled: s.Guards.CatchUp.Enabled, Window: s.Guards.CatchUp.Window}, Maintenance: string(s.Guards.Maintenance)},
		Limit:  s.Overrides.Limit, ExtraVars: schedules.MaskSealed(s.Overrides.ExtraVars),
		Paused: s.Paused, NextRunAt: s.NextRunAt, LastRunAt: s.LastRunAt,
		Revision: s.Revision, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if s.Simple != nil {
		dto.Simple = &simpleDTO{Kind: s.Simple.Kind, Every: s.Simple.Every, Time: s.Simple.Time, Days: s.Simple.Days, Day: s.Simple.Day}
		dto.Description = s.Simple.Describe()
	}
	if last != nil {
		r := toScheduleRunDTO(*last)
		dto.LastRun = &r
	}
	for _, c := range notifyOn {
		dto.NotifyOn = append(dto.NotifyOn, string(c))
	}
	return dto
}

func toScheduleRunDTO(r schedules.Run) scheduleRunDTO {
	dto := scheduleRunDTO{ID: r.ID.String(), DueAt: r.DueAt, DecidedAt: r.DecidedAt, Outcome: string(r.Outcome), Reason: r.Reason}
	if r.JobGroupID != nil {
		dto.JobGroupID = r.JobGroupID.String()
	}
	return dto
}

func (b ScheduleBody) simple() *cronexpr.Simple {
	if b.Simple == nil {
		return nil
	}
	return &cronexpr.Simple{Kind: b.Simple.Kind, Every: b.Simple.Every, Time: b.Simple.Time, Days: b.Simple.Days, Day: b.Simple.Day}
}

func (b ScheduleBody) guards() schedules.Guards {
	if b.Guards == nil {
		return schedules.Guards{}
	}
	return schedules.Guards{Overlap: schedules.Overlap(b.Guards.Overlap), CatchUp: schedules.CatchUp{Enabled: b.Guards.CatchUp.Enabled, Window: b.Guards.CatchUp.Window}, Maintenance: schedules.Maintenance(b.Guards.Maintenance)}
}
