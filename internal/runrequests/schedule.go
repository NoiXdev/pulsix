package runrequests

import (
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/schedules"
)

// ScheduleAction is what a schedule request does (spec 2026-11-25
// assistant API gaps, C, decision D1).
type ScheduleAction string

// The schedule actions: create a schedule, change one, resume a paused one.
// Pausing and deleting need no request: they only stop runs.
const (
	ScheduleCreate ScheduleAction = "create"
	ScheduleUpdate ScheduleAction = "update"
	ScheduleResume ScheduleAction = "resume"
)

// ScheduleSpec is a schedule as a request asks for it: the fields of the
// schedule REST body. Its extra vars never hold a secret; on an update a
// secret field the schedule keeps holds the masked marker
// {"$secret":"set"}, which the approval resolves against the stored value
// at the same path. Numbers in ExtraVars are json.Number, so they stay
// exact.
type ScheduleSpec struct {
	TemplateID uuid.UUID        `json:"template_id"`
	Name       string           `json:"name"`
	Mode       schedules.Mode   `json:"mode"`
	Cron       string           `json:"cron,omitempty"`
	Simple     *cronexpr.Simple `json:"simple,omitempty"`
	Timezone   string           `json:"timezone"`
	Guards     schedules.Guards `json:"guards"`
	Limit      *string          `json:"limit,omitempty"`
	ExtraVars  map[string]any   `json:"extra_vars,omitempty"`
}

// SchedulePatch is an update request's change: a nil field keeps the
// schedule's value. Cron without Mode switches to cron mode, Simple
// without Mode to simple mode. An empty Limit clears the limit; ExtraVars,
// when given (an empty object included), is the complete new set.
type SchedulePatch struct {
	TemplateID *uuid.UUID
	Name       *string
	Mode       *schedules.Mode
	Cron       *string
	Simple     *cronexpr.Simple
	Timezone   *string
	Guards     *schedules.Guards
	Limit      *string
	ExtraVars  map[string]any
}

// ScheduleInput is what a schedule request asks for: Spec for a create,
// ScheduleID and Patch for an update, ScheduleID for a resume.
type ScheduleInput struct {
	Action     ScheduleAction
	ScheduleID uuid.UUID
	Spec       *ScheduleSpec
	Patch      *SchedulePatch
}

// ScheduleChange is what a stored schedule request does, the request's
// payload: the action and, for a create or an update, the schedule as it
// will be (an update's patch merged into the schedule when the request was
// made). AssistantOverrides says the limit and extra vars are the
// assistant's (a create with any, an update that set either): the
// schedule then carries them untrusted (schedules.Input.Untrusted).
type ScheduleChange struct {
	Action             ScheduleAction `json:"action"`
	Spec               *ScheduleSpec  `json:"spec,omitempty"`
	AssistantOverrides bool           `json:"assistant_overrides,omitempty"`
}

// PictureSchedule is the picture of a schedule request: the schedule as it
// will be (New, a create and an update) and as it is now (Current, an
// update and a resume), so the person sees what changes. Gone is set when
// the schedule an update or resume names no longer exists; such a request
// cannot be approved.
type PictureSchedule struct {
	Action     ScheduleAction `json:"action" enum:"create,update,resume" doc:"create a schedule, change one, or resume a paused one"`
	ScheduleID string         `json:"schedule_id" doc:"The schedule the request creates (its id is taken when the request is made), changes or resumes"`
	New        *ScheduleState `json:"new,omitempty" doc:"The schedule as the approval stores it (create and update)"`
	Current    *ScheduleState `json:"current,omitempty" doc:"The schedule as it is now (update and resume)"`
	// AssistantOverrides says New's limit and extra vars are the
	// assistant's: the runs take every text in them literally.
	AssistantOverrides bool `json:"assistant_overrides,omitempty" doc:"The limit and extra vars are the assistant's: its runs take every text in them literally, never as a template"`
	Gone               bool `json:"gone,omitempty" doc:"The schedule no longer exists; the request cannot be approved"`
}

// ScheduleState is one schedule in a picture. Secrets appear as the
// masked marker {"$secret":"set"}.
type ScheduleState struct {
	TemplateID   string           `json:"template_id"`
	TemplateName string           `json:"template_name"`
	Name         string           `json:"name"`
	Mode         string           `json:"mode" enum:"cron,simple"`
	Cron         string           `json:"cron"`
	Description  string           `json:"description" doc:"Human text for the timetable; empty when there is none"`
	Simple       *cronexpr.Simple `json:"simple,omitempty"`
	Timezone     string           `json:"timezone"`
	Guards       schedules.Guards `json:"guards"`
	Limit        *string          `json:"limit,omitempty"`
	ExtraVars    map[string]any   `json:"extra_vars,omitempty"`
	Paused       bool             `json:"paused"`
	// Revision is the stored schedule's (Current only): a change to the
	// schedule changes the picture, and an update approves exactly the
	// revision the person saw.
	Revision int64 `json:"revision,omitempty"`
}
