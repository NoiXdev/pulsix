// Schedule request routes (spec 2026-11-25 assistant API gaps, C, decision
// D1): an assistant asks for a schedule to be created, changed or resumed
// (automation.request), one route per MCP tool; a person approves it like a
// run request, on the run request routes. Pausing and deleting a schedule
// need no request: automation.request reaches those routes directly.
package api

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// ScheduleRequestBody asks for a new schedule: the schedule body, the
// reason and the request's expiry.
type ScheduleRequestBody struct {
	TemplateID string      `json:"template_id" format:"uuid"`
	Name       string      `json:"name" minLength:"1" maxLength:"100"`
	Mode       string      `json:"mode" enum:"cron,simple"`
	Cron       string      `json:"cron,omitempty" maxLength:"200"`
	Simple     *simpleDTO  `json:"simple,omitempty"`
	Timezone   string      `json:"timezone" minLength:"1" maxLength:"64"`
	Guards     *guardsDTO  `json:"guards,omitempty"`
	Limit      *string     `json:"limit,omitempty" maxLength:"500" doc:"Host patterns replacing the template's limit (no @file, regex or subscript patterns)"`
	ExtraVars  exactObject `json:"extra_vars,omitempty" doc:"Extra vars replacing the template's top-level ones of the same name, checked against the template's schema (the template must declare its extra vars). Secret fields and names Ansible or Pulsix act on (ansible_…, pulsix_…, Ansible's magic variables and playbook keywords) cannot be set here; the schedule's runs receive every text in them as literal text, never as a template"`
	Reason     string      `json:"reason" maxLength:"300" doc:"Why the schedule is needed, shown to the person who approves it"`
	ExpiresIn  int         `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

// ScheduleUpdateRequestBody asks for a change of a schedule: every field
// left out keeps the schedule's value.
type ScheduleUpdateRequestBody struct {
	TemplateID *string     `json:"template_id,omitempty" format:"uuid"`
	Name       *string     `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Mode       *string     `json:"mode,omitempty" enum:"cron,simple" doc:"Left out, cron switches to cron mode and simple to simple mode"`
	Cron       *string     `json:"cron,omitempty" maxLength:"200"`
	Simple     *simpleDTO  `json:"simple,omitempty"`
	Timezone   *string     `json:"timezone,omitempty" minLength:"1" maxLength:"64"`
	Guards     *guardsDTO  `json:"guards,omitempty"`
	Limit      *string     `json:"limit,omitempty" maxLength:"500" doc:"Host patterns replacing the template's limit; an empty text removes the schedule's limit"`
	ExtraVars  exactObject `json:"extra_vars,omitempty" doc:"The complete new set of the schedule's extra vars (left out keeps them). A secret field the schedule holds is kept with {\"$secret\":\"keep\"} and dropped when left out. Extra vars replacing the template's top-level ones of the same name, checked against the template's schema (the template must declare its extra vars). Secret fields and names Ansible or Pulsix act on (ansible_…, pulsix_…, Ansible's magic variables and playbook keywords) cannot be set here; the schedule's runs receive every text in them as literal text, never as a template"`
	Reason     string      `json:"reason" maxLength:"300" doc:"Why the change is needed, shown to the person who approves it"`
	ExpiresIn  int         `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

type scheduleResumeRequestBody struct {
	Reason    string `json:"reason" maxLength:"300" doc:"Why the schedule should run again, shown to the person who approves it"`
	ExpiresIn int    `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

func simpleOf(d *simpleDTO) *cronexpr.Simple {
	if d == nil {
		return nil
	}
	return &cronexpr.Simple{Kind: d.Kind, Every: d.Every, Time: d.Time, Days: d.Days, Day: d.Day}
}

func guardsOf(d *guardsDTO) schedules.Guards {
	if d == nil {
		return schedules.Guards{}
	}
	return schedules.Guards{Overlap: schedules.Overlap(d.Overlap), CatchUp: schedules.CatchUp{Enabled: d.CatchUp.Enabled, Window: d.CatchUp.Window},
		Maintenance: schedules.Maintenance(d.Maintenance)}
}

func (b ScheduleRequestBody) input() (runrequests.CreateInput, error) {
	tmpl, err := parseUUID(b.TemplateID, "template_id")
	if err != nil {
		return runrequests.CreateInput{}, err
	}
	vars, err := b.ExtraVars.decode()
	if err != nil {
		return runrequests.CreateInput{}, err
	}
	spec := &runrequests.ScheduleSpec{
		TemplateID: tmpl, Name: b.Name, Mode: schedules.Mode(b.Mode), Cron: b.Cron, Simple: simpleOf(b.Simple),
		Timezone: b.Timezone, Guards: guardsOf(b.Guards), Limit: b.Limit, ExtraVars: vars,
	}
	return runrequests.CreateInput{Kind: runrequests.KindSchedule, Reason: b.Reason, ExpiresInMinutes: b.ExpiresIn,
		Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: spec}}, nil
}

func (b ScheduleUpdateRequestBody) input(id uuid.UUID) (runrequests.CreateInput, error) {
	patch := &runrequests.SchedulePatch{Name: b.Name, Cron: b.Cron, Simple: simpleOf(b.Simple), Timezone: b.Timezone, Limit: b.Limit}
	if b.TemplateID != nil {
		tmpl, err := parseUUID(*b.TemplateID, "template_id")
		if err != nil {
			return runrequests.CreateInput{}, err
		}
		patch.TemplateID = &tmpl
	}
	if b.Mode != nil {
		m := schedules.Mode(*b.Mode)
		patch.Mode = &m
	}
	if b.Guards != nil {
		g := guardsOf(b.Guards)
		patch.Guards = &g
	}
	vars, err := b.ExtraVars.decode()
	if err != nil {
		return runrequests.CreateInput{}, err
	}
	patch.ExtraVars = vars
	return runrequests.CreateInput{Kind: runrequests.KindSchedule, Reason: b.Reason, ExpiresInMinutes: b.ExpiresIn,
		Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleUpdate, ScheduleID: id, Patch: patch}}, nil
}

// registerScheduleRequestRoutes wires the schedule request routes.
func registerScheduleRequestRoutes(hapi huma.API, deps Deps) {
	create := func(ctx context.Context, scope Scope, in runrequests.CreateInput) (*runRequestDetailOutput, error) {
		in.RequestedByUser, in.RequestedByToken = scope.UserID, scope.TokenID
		r, err := deps.RunRequests.Create(ctx, scope.OrgID, in)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), scheduleRequestSummary(r))
		return runRequestDetail(ctx, deps, scope, r.ID)
	}
	created := func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }

	huma.Post(hapi, "/api/v1/orgs/{org}/schedule-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body ScheduleRequestBody
	}) (*runRequestDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAutomationRequest)
		if err != nil {
			return nil, err
		}
		cin, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		return create(ctx, scope, cin)
	}, created, withAudit("requestSchedule", "run_request"))

	huma.Post(hapi, "/api/v1/orgs/{org}/schedules/{id}/update-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body ScheduleUpdateRequestBody
	}) (*runRequestDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAutomationRequest)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "schedule id")
		if err != nil {
			return nil, err
		}
		cin, err := in.Body.input(id)
		if err != nil {
			return nil, err
		}
		return create(ctx, scope, cin)
	}, created, withAudit("requestScheduleUpdate", "run_request"))

	huma.Post(hapi, "/api/v1/orgs/{org}/schedules/{id}/resume-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body scheduleResumeRequestBody
	}) (*runRequestDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAutomationRequest)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "schedule id")
		if err != nil {
			return nil, err
		}
		return create(ctx, scope, runrequests.CreateInput{Kind: runrequests.KindSchedule, Reason: in.Body.Reason, ExpiresInMinutes: in.Body.ExpiresIn,
			Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleResume, ScheduleID: id}})
	}, created, withAudit("requestScheduleResume", "run_request"))
}

// scheduleRequestSummary is the audit summary of a schedule request: what
// it does to which schedule, the overrides by field (extra vars by their
// keys, never a value), and the reason.
func scheduleRequestSummary(r runrequests.Request) string {
	ch := r.Schedule
	summary := "requested to " + string(ch.Action) + " schedule " + uuidString(r.TargetID)
	if ch.Spec != nil {
		summary += " (" + strconv.Quote(ch.Spec.Name) + ", job template " + ch.Spec.TemplateID.String()
		if ch.AssistantOverrides {
			var fields []string
			if ch.Spec.Limit != nil {
				fields = append(fields, "limit")
			}
			if len(ch.Spec.ExtraVars) > 0 {
				fields = append(fields, "extra_vars ("+strings.Join(slices.Sorted(maps.Keys(ch.Spec.ExtraVars)), ", ")+")")
			}
			if len(fields) > 0 {
				summary += "; overrides: " + strings.Join(fields, ", ")
			}
		}
		summary += ")"
	}
	return summary + ": " + r.Reason
}

// auditAppliedSchedule records what an approved schedule request did to
// the schedule in the schedule's own feed, as the schedule routes would,
// attributed to the approver.
func auditAppliedSchedule(ctx context.Context, deps Deps, scope Scope, a runrequests.Approved) {
	ps := a.Schedule
	if ps == nil {
		return
	}
	var action, verb, name string
	switch ps.Action {
	case runrequests.ScheduleCreate:
		action, verb = "createSchedule", "created"
	case runrequests.ScheduleUpdate:
		action, verb = "updateSchedule", "updated"
	default:
		action, verb = "resumeSchedule", "resumed"
	}
	switch {
	case ps.New != nil:
		name = ps.New.Name
	case ps.Current != nil:
		name = ps.Current.Name
	}
	recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" "+action, "schedule", ps.ScheduleID,
		verb+" schedule "+name+" (approved request "+a.Request.ID.String()+")")
}

// scheduleApprovalSummary is the audit summary of a schedule request's
// approval.
func scheduleApprovalSummary(a runrequests.Approved) string {
	return "approved schedule request (" + string(a.Schedule.Action) + " schedule " + a.Schedule.ScheduleID +
		"; template revision " + strconv.FormatInt(a.TemplateRevision, 10) + ")"
}
