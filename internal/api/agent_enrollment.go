package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// Agent enrollment inside an approved run (spec 2026-11-25 assistant API
// gaps, B, "Who may"): a run of a template with agent enrollment mints
// enrollment tokens, which needs agents.write. So switching a template's
// flag, approving a run request of such a template, starting, retrying or
// dry-running one by hand, and creating, changing or resuming a schedule
// of one need agents.write in addition to the route's own permission. A
// webhook trigger of such a template is not refused: its runs mint
// nothing.

// enrolmentRefused is the 403 for a run or schedule of a template with
// agent enrollment.
const enrolmentRefused = "this job template enrols agents on the hosts it runs on; starting, approving or scheduling its runs needs the agents.write permission"

// mayEnrolAgents reports whether scope holds agents.write: its role does,
// and for an assistant token also one of its areas (none grants it).
func mayEnrolAgents(scope Scope) bool {
	if !tenancy.Allows(scope.Role, tenancy.PermAgentsWrite) {
		return false
	}
	if scope.TokenKind == auth.TokenKindAssistant {
		return tenancy.AreasAllow(scope.Areas, tenancy.PermAgentsWrite)
	}
	return true
}

// requireEnrolment refuses, without agents.write, a run or schedule of
// templateID when that template enrols agents. A template that does not
// exist passes: the route's own call answers for it.
func requireEnrolment(ctx context.Context, deps Deps, scope Scope, templateID uuid.UUID) error {
	if mayEnrolAgents(scope) {
		return nil
	}
	t, err := deps.Templates.Get(ctx, scope.OrgID, templateID)
	if errors.Is(err, templates.ErrNotFound) {
		return nil
	}
	if err != nil {
		return mapTemplatesErr(ctx, err)
	}
	if t.AgentEnrollment {
		return huma.Error403Forbidden(enrolmentRefused)
	}
	return nil
}

// requireRetryEnrolment is requireEnrolment for a retry of the job jobID,
// a run of its template. A job that does not exist, or has no template,
// passes: the retry answers for it.
func requireRetryEnrolment(ctx context.Context, deps Deps, scope Scope, jobID uuid.UUID) error {
	if mayEnrolAgents(scope) {
		return nil
	}
	j, err := deps.Jobs.Get(ctx, scope.OrgID, jobID)
	if errors.Is(err, jobs.ErrNotFound) || (err == nil && j.TemplateID == nil) {
		return nil
	}
	if err != nil {
		return mapJobsErr(ctx, err)
	}
	return requireEnrolment(ctx, deps, scope, *j.TemplateID)
}

// requireScheduleEnrolment is requireEnrolment for the schedule id's
// template. A schedule that does not exist passes: the route answers for
// it.
func requireScheduleEnrolment(ctx context.Context, deps Deps, scope Scope, id uuid.UUID) error {
	if mayEnrolAgents(scope) {
		return nil
	}
	s, err := deps.Schedules.Get(ctx, scope.OrgID, id)
	if errors.Is(err, schedules.ErrNotFound) {
		return nil
	}
	if err != nil {
		return mapSchedulesErr(ctx, err)
	}
	return requireEnrolment(ctx, deps, scope, s.TemplateID)
}
