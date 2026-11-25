package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agentenroll"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// Agent enrollment inside a run (spec 2026-11-25 assistant API gaps, B;
// controller rulings of fix round 1). A run mints enrollment tokens only
// for the hosts a signed-in person confirmed: the approval of a run
// request (the picture's list), or the person who starts a manual run or
// retry of a template with agent enrollment, who sends the hosts they
// confirmed as enroll_host_ids (the preview routes below list them).
// Switching the flag, approving such a request and starting or retrying
// such a run need a person signed in with agents.write (mayEnrolAgents).
// Schedules, chained follow-ups, webhook runs and dry runs carry no list
// and mint nothing, so they need nothing more.

// mayEnrolAgents reports whether scope may enrol agents: a person signed
// in (a bearer token never does — credentials do not mint credentials,
// backlog 7.49) whose role holds agents.write.
func mayEnrolAgents(scope Scope) bool {
	return scope.TokenID == nil && tenancy.Allows(scope.Role, tenancy.PermAgentsWrite)
}

// enrollHostDTO is a host a run enrols the agent on.
type enrollHostDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toEnrollHostDTOs(hs []agentenroll.Host) []enrollHostDTO {
	out := make([]enrollHostDTO, len(hs))
	for i, h := range hs {
		out[i] = enrollHostDTO{ID: h.ID.String(), Name: h.Name}
	}
	return out
}

type enrollmentPreviewOutput struct {
	Body struct {
		Enrols bool            `json:"enrols" doc:"Whether the template enrols agents; when it does, a run or retry sends hosts' ids as enroll_host_ids"`
		Hosts  []enrollHostDTO `json:"hosts" doc:"The hosts a run started now enrols the agent on: its targets without an agent"`
	}
}

func enrollmentPreview(p agentenroll.Preview) *enrollmentPreviewOutput {
	out := &enrollmentPreviewOutput{}
	out.Body.Enrols, out.Body.Hosts = p.Enrols, toEnrollHostDTOs(p.Hosts)
	return out
}

// parseEnrollHostIDs reads the confirmed list; nil when none was sent.
func parseEnrollHostIDs(raw *[]string) (*[]uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(*raw))
	for i, s := range *raw {
		id, err := parseUUID(s, "enroll_host_ids")
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return &ids, nil
}

// mapEnrollErr maps the confirmation's refusals: 403 for a caller who may
// not enrol, 409 for a list to confirm (with the current hosts in the
// detail's value) or one sent for a template that does not enrol.
func mapEnrollErr(ctx context.Context, err error) error {
	var ce *agentenroll.ConfirmError
	switch {
	case errors.As(err, &ce):
		return &enrollConfirmProblem{
			ErrorModel: huma.ErrorModel{
				Title: http.StatusText(http.StatusConflict), Status: http.StatusConflict, Detail: ce.Error(),
				Errors: []*huma.ErrorDetail{{Location: "body.enroll_host_ids", Message: "confirm the hosts the run enrols the agent on"}},
			},
			EnrollHosts: toEnrollHostDTOs(ce.Current),
		}
	case errors.Is(err, agentenroll.ErrNotAllowed):
		return huma.Error403Forbidden(err.Error())
	case errors.Is(err, agentenroll.ErrNotEnrolling):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, agentenroll.ErrNotFound):
		return huma.Error404NotFound("not found")
	}
	return mapJobsErr(ctx, err)
}

// enrollConfirmProblem is the 409 asking a person to confirm the hosts a
// run enrols: the problem document plus enroll_hosts, the hosts it enrols
// now (errors[].value is cleared on every response, redactErrorValues).
type enrollConfirmProblem struct {
	huma.ErrorModel
	EnrollHosts []enrollHostDTO `json:"enroll_hosts"`
}

type templateEnrollmentPreviewInput struct {
	Org    string `path:"org"`
	ID     string `path:"id"`
	HostID string `query:"host_id" doc:"Preview a run on this one host only"`
}

// registerEnrollmentRoutes wires the previews a person confirms before
// starting or retrying a run of a template with agent enrollment.
func registerEnrollmentRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/job-templates/{id}/enrollment-preview", func(ctx context.Context, in *templateEnrollmentPreviewInput) (*enrollmentPreviewOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		var onlyHost *uuid.UUID
		if in.HostID != "" {
			h, err := parseUUID(in.HostID, "host_id")
			if err != nil {
				return nil, err
			}
			onlyHost = &h
		}
		p, err := deps.AgentEnroll.RunPreview(ctx, scope.OrgID, id, onlyHost)
		if err != nil {
			return nil, mapEnrollErr(ctx, err)
		}
		return enrollmentPreview(p), nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/jobs/{id}/enrollment-preview", func(ctx context.Context, in *jobGetInput) (*enrollmentPreviewOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		p, err := deps.AgentEnroll.RetryPreview(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapEnrollErr(ctx, err)
		}
		return enrollmentPreview(p), nil
	})
}
