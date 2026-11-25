package mcp

import (
	"context"
	"net/http"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// The automation tools (spec 2026-11-25 assistant API gaps, C, decision
// D1): creating, changing or resuming a schedule is a request a person
// approves, like a run; pausing and deleting one only stop runs and act
// directly. All of them need automation.request.

type simpleArg struct {
	Kind  string   `json:"kind" jsonschema:"every_minutes, every_hours, daily, weekly or monthly"`
	Every int      `json:"every,omitempty" jsonschema:"every_minutes and every_hours: the interval"`
	Time  string   `json:"time,omitempty" jsonschema:"HH:MM in the schedule's time zone (daily, weekly, monthly)"`
	Days  []string `json:"days,omitempty" jsonschema:"weekly: the days, mon to sun"`
	Day   int      `json:"day,omitempty" jsonschema:"monthly: the day, 1 to 31; months without it are skipped"`
}

type catchUpArg struct {
	Enabled bool   `json:"enabled" jsonschema:"run once late after a downtime, inside the window"`
	Window  string `json:"window,omitempty" jsonschema:"how late, a duration from 1m to 24h (default 1h)"`
}

type guardsArg struct {
	Overlap     string      `json:"overlap,omitempty" jsonschema:"skip (default) or queue: what a due run does while a run of the template is active"`
	CatchUp     *catchUpArg `json:"catch_up,omitempty"`
	Maintenance string      `json:"maintenance,omitempty" jsonschema:"ignore (default), skip, or exclude_hosts: what a due run does while a targeted host is in a maintenance window"`
}

type scheduleCreateArgs struct {
	TemplateID string         `json:"template_id" jsonschema:"the job template the schedule runs"`
	Name       string         `json:"name" jsonschema:"the schedule's name, unique in the organization"`
	Mode       string         `json:"mode" jsonschema:"cron (give cron) or simple (give simple)"`
	Cron       string         `json:"cron,omitempty" jsonschema:"a five-field cron expression"`
	Simple     *simpleArg     `json:"simple,omitempty" jsonschema:"the timetable as a simple shape"`
	Timezone   string         `json:"timezone" jsonschema:"an IANA time zone such as Europe/Berlin or UTC"`
	Guards     *guardsArg     `json:"guards,omitempty"`
	Limit      *string        `json:"limit,omitempty" jsonschema:"host patterns replacing the template's limit (no @file, regex or subscript patterns)"`
	ExtraVars  map[string]any `json:"extra_vars,omitempty" jsonschema:"extra vars replacing the template's top-level ones of the same name; the template must declare them. Never secrets, and never names Ansible or Pulsix act on (ansible_..., pulsix_..., magic variables, playbook keywords); the schedule's runs take every text in them literally, never as a template"`
	Reason     string         `json:"reason" jsonschema:"one line on why; the person approving reads it"`
	ExpiresIn  int            `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

type scheduleUpdateArgs struct {
	ScheduleID string         `json:"schedule_id" jsonschema:"the schedule to change"`
	TemplateID string         `json:"template_id,omitempty" jsonschema:"another job template to run"`
	Name       string         `json:"name,omitempty"`
	Mode       string         `json:"mode,omitempty" jsonschema:"cron or simple; left out, cron switches to cron mode and simple to simple mode"`
	Cron       string         `json:"cron,omitempty" jsonschema:"a five-field cron expression"`
	Simple     *simpleArg     `json:"simple,omitempty"`
	Timezone   string         `json:"timezone,omitempty" jsonschema:"an IANA time zone"`
	Guards     *guardsArg     `json:"guards,omitempty" jsonschema:"the complete new guards"`
	Limit      *string        `json:"limit,omitempty" jsonschema:"host patterns replacing the template's limit; an empty text removes the schedule's limit"`
	ExtraVars  map[string]any `json:"extra_vars,omitempty" jsonschema:"the complete new set of the schedule's extra vars (left out keeps them); a secret the schedule holds is kept with {\"$secret\":\"keep\"} and dropped when left out. Never new secrets, and never names Ansible or Pulsix act on; the runs take every text literally"`
	Reason     string         `json:"reason" jsonschema:"one line on why; the person approving reads it"`
	ExpiresIn  int            `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

type scheduleResumeArgs struct {
	ScheduleID string `json:"schedule_id" jsonschema:"the paused schedule"`
	Reason     string `json:"reason" jsonschema:"one line on why; the person approving reads it"`
	ExpiresIn  int    `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

// putIf sets key when v is not empty.
func putIf(body map[string]any, key, v string) {
	if v != "" {
		body[key] = v
	}
}

// scheduleBody is the REST body the create and update tools share.
func scheduleBody(reason string, expiresIn int, simple *simpleArg, guards *guardsArg, limit *string, vars map[string]any) map[string]any {
	body := map[string]any{"reason": reason}
	if expiresIn != 0 {
		body["expires_in"] = expiresIn
	}
	if simple != nil {
		body["simple"] = simple
	}
	if guards != nil {
		body["guards"] = guards
	}
	if limit != nil {
		body["limit"] = *limit
	}
	if vars != nil {
		body["extra_vars"] = vars
	}
	return body
}

// scheduleNote is what a schedule request's result tells the assistant.
const scheduleNote = "Nothing changes until a person approves this request at approval_url; the approval then creates, changes or resumes the schedule exactly as the picture shows. Give them the link."

func automationTools() []toolDef {
	const area = tenancy.AreaAutomationRequest
	return []toolDef{
		defineExact("create_schedule", "Create a schedule", "Ask for a new schedule of a job template: a cron expression or a simple shape in a time zone, the guards (overlap, catch-up, maintenance), and optionally a limit and non-secret extra vars. Nothing is created until a person approves the request in Pulsix; the result carries the link to give them and the picture they see. Requests expire after an hour unless expires_in says otherwise.",
			area, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in scheduleCreateArgs) (any, error) {
				body := scheduleBody(in.Reason, in.ExpiresIn, in.Simple, in.Guards, in.Limit, in.ExtraVars)
				body["template_id"], body["name"], body["mode"], body["timezone"] = in.TemplateID, in.Name, in.Mode, in.Timezone
				putIf(body, "cron", in.Cron)
				return c.requestAt(ctx, "/schedule-requests", nil, body, scheduleNote)
			}),
		defineExact("update_schedule", "Change a schedule", "Ask for a change of a schedule: give only the fields that change. The picture shows the schedule as it is next to how it will be; a paused schedule stays paused. Nothing changes until a person approves the request in Pulsix.",
			area, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in scheduleUpdateArgs) (any, error) {
				body := scheduleBody(in.Reason, in.ExpiresIn, in.Simple, in.Guards, in.Limit, in.ExtraVars)
				putIf(body, "template_id", in.TemplateID)
				putIf(body, "name", in.Name)
				putIf(body, "mode", in.Mode)
				putIf(body, "cron", in.Cron)
				putIf(body, "timezone", in.Timezone)
				return c.requestAt(ctx, "/schedules/%s/update-requests", []string{in.ScheduleID}, body, scheduleNote)
			}),
		defineExact("resume_schedule", "Resume a schedule", "Ask for a paused schedule to run again, from now on (a long pause never replays). Nothing runs until a person approves the request in Pulsix. A schedule your change of its job template or repository paused (paused_reason assistant_change) is resumed only by a person approving its review request; asking is refused.",
			area, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in scheduleResumeArgs) (any, error) {
				body := map[string]any{"reason": in.Reason}
				if in.ExpiresIn != 0 {
					body["expires_in"] = in.ExpiresIn
				}
				return c.requestAt(ctx, "/schedules/%s/resume-requests", []string{in.ScheduleID}, body, scheduleNote)
			}),
		define("pause_schedule", "Pause a schedule", "Pause a schedule at once: it starts no run until it is resumed (resuming needs a person's approval, see resume_schedule). Runs already started go on.",
			area, writeTool, func(ctx context.Context, c *caller, in scheduleInput) (any, error) {
				return send(ctx, c, http.MethodPost, nil, "", "/schedules/%s/pause", in.ScheduleID)
			}),
		define("delete_schedule", "Delete a schedule", "Delete a schedule at once, with its run history. Runs already started go on.",
			area, writeTool, func(ctx context.Context, c *caller, in scheduleInput) (any, error) {
				return send(ctx, c, http.MethodDelete, nil, "", "/schedules/%s", in.ScheduleID)
			}),
	}
}
