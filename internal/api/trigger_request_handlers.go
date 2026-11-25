// Trigger request routes (spec 2026-11-25 assistant API gaps, C, decision
// D1): an assistant asks for a webhook trigger to be created, changed or
// given a new secret (automation.request), one route per MCP tool; a
// person approves it like a run request, on the run request routes, and
// only that person's approval response carries the trigger's secret.
// Disabling and deleting a trigger need no request: automation.request
// reaches those routes directly.
package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
)

// TriggerRequestBody asks for a new webhook trigger: the trigger body, the
// reason and the request's expiry.
type TriggerRequestBody struct {
	Kind            string             `json:"kind" enum:"template_run,repo_sync"`
	TemplateID      string             `json:"template_id,omitempty" doc:"The job template a template_run trigger runs"`
	RepoID          string             `json:"repo_id,omitempty" doc:"The repository a repo_sync trigger syncs"`
	Name            string             `json:"name" minLength:"1" maxLength:"100"`
	Enabled         *bool              `json:"enabled,omitempty" doc:"default true: the trigger takes calls once a person approves the request"`
	Preset          string             `json:"preset" enum:"pulsix,github,gitea,gitlab,bearer"`
	TimestampHeader string             `json:"timestamp_header,omitempty" maxLength:"64"`
	AllowCIDRs      []string           `json:"allow_cidrs,omitempty" maxItems:"50" doc:"Addresses senders may call from; empty allows any"`
	Mapping         *triggerMappingDTO `json:"mapping,omitempty" doc:"Payload JSONPaths onto extra vars (never a secret field or a name Ansible or Pulsix acts on) and the limit; a run receives the values as untrusted text"`
	OnActive        string             `json:"on_active,omitempty" enum:"queue,skip"`
	RateLimit       *rateLimitDTO      `json:"rate_limit,omitempty"`
	Reason          string             `json:"reason" maxLength:"300" doc:"Why the trigger is needed, shown to the person who approves it. The trigger's secret is drawn when a person approves the request and shown to that person only; it never appears here"`
	ExpiresIn       int                `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

// TriggerUpdateRequestBody asks for a change of a webhook trigger: every
// field left out keeps the trigger's value. Its kind and target never
// change.
type TriggerUpdateRequestBody struct {
	Name            *string            `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Enabled         *bool              `json:"enabled,omitempty" doc:"Left out, the trigger stays as enabled or disabled as it is when the request is approved"`
	Preset          *string            `json:"preset,omitempty" enum:"pulsix,github,gitea,gitlab,bearer"`
	TimestampHeader *string            `json:"timestamp_header,omitempty" maxLength:"64" doc:"An empty text removes it"`
	AllowCIDRs      []string           `json:"allow_cidrs,omitempty" maxItems:"50" doc:"The complete new list (an empty list allows any address); left out keeps it"`
	Mapping         *triggerMappingDTO `json:"mapping,omitempty" doc:"The complete new mapping; left out keeps it"`
	OnActive        *string            `json:"on_active,omitempty" enum:"queue,skip"`
	RateLimit       *rateLimitDTO      `json:"rate_limit,omitempty"`
	Reason          string             `json:"reason" maxLength:"300" doc:"Why the change is needed, shown to the person who approves it"`
	ExpiresIn       int                `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

type triggerRotateRequestBody struct {
	Reason    string `json:"reason" maxLength:"300" doc:"Why the trigger needs a new secret, shown to the person who approves it. The new secret is drawn when a person approves the request and shown to that person only; it never appears here"`
	ExpiresIn int    `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

func (b TriggerRequestBody) input() (runrequests.CreateInput, error) {
	in, err := triggerInput(TriggerBody{
		Kind: b.Kind, TemplateID: b.TemplateID, RepoID: b.RepoID, Name: b.Name, Enabled: b.Enabled, Preset: b.Preset,
		TimestampHeader: b.TimestampHeader, AllowCIDRs: b.AllowCIDRs, Mapping: b.Mapping, OnActive: b.OnActive, RateLimit: b.RateLimit,
	})
	if err != nil {
		return runrequests.CreateInput{}, err
	}
	spec := &runrequests.TriggerSpec{
		Kind: in.Kind, TemplateID: in.TemplateID, RepoID: in.RepoID, Name: in.Name, Enabled: in.Enabled, Preset: in.Preset,
		TimestampHeader: in.TimestampHeader, AllowCIDRs: in.AllowCIDRs, Mapping: in.Mapping, OnActive: in.OnActive, RateLimit: in.RateLimit,
	}
	return runrequests.CreateInput{Kind: runrequests.KindTrigger, Reason: b.Reason, ExpiresInMinutes: b.ExpiresIn,
		Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: spec}}, nil
}

func (b TriggerUpdateRequestBody) input(id uuid.UUID) runrequests.CreateInput {
	patch := &runrequests.TriggerPatch{Name: b.Name, Enabled: b.Enabled, TimestampHeader: b.TimestampHeader}
	if b.Preset != nil {
		p := triggers.Preset(*b.Preset)
		patch.Preset = &p
	}
	if b.AllowCIDRs != nil {
		cidrs := b.AllowCIDRs
		patch.AllowCIDRs = &cidrs
	}
	if b.Mapping != nil {
		m := triggers.Mapping{ExtraVars: b.Mapping.ExtraVars, Limit: b.Mapping.Limit}
		patch.Mapping = &m
	}
	if b.OnActive != nil {
		o := triggers.OnActive(*b.OnActive)
		patch.OnActive = &o
	}
	if b.RateLimit != nil {
		rl := triggers.RateLimit{Count: b.RateLimit.Count, Per: b.RateLimit.Per}
		patch.RateLimit = &rl
	}
	return runrequests.CreateInput{Kind: runrequests.KindTrigger, Reason: b.Reason, ExpiresInMinutes: b.ExpiresIn,
		Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: id, Patch: patch}}
}

// registerTriggerRequestRoutes wires the trigger request routes.
func registerTriggerRequestRoutes(hapi huma.API, deps Deps) {
	create := func(ctx context.Context, scope Scope, in runrequests.CreateInput) (*runRequestDetailOutput, error) {
		in.RequestedByUser, in.RequestedByToken = scope.UserID, scope.TokenID
		r, err := deps.RunRequests.Create(ctx, scope.OrgID, in)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), triggerRequestSummary(r))
		return runRequestDetail(ctx, deps, scope, r.ID)
	}
	created := func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }
	scoped := func(ctx context.Context, org, rawID string) (Scope, uuid.UUID, error) {
		scope, err := requireOrg(ctx, deps, org, tenancy.PermAutomationRequest)
		if err != nil {
			return Scope{}, uuid.Nil, err
		}
		id, err := parseUUID(rawID, "trigger id")
		return scope, id, err
	}

	huma.Post(hapi, "/api/v1/orgs/{org}/trigger-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body TriggerRequestBody
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
	}, created, withAudit("requestTrigger", "run_request"))

	huma.Post(hapi, "/api/v1/orgs/{org}/triggers/{id}/update-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body TriggerUpdateRequestBody
	}) (*runRequestDetailOutput, error) {
		scope, id, err := scoped(ctx, in.Org, in.ID)
		if err != nil {
			return nil, err
		}
		return create(ctx, scope, in.Body.input(id))
	}, created, withAudit("requestTriggerUpdate", "run_request"))

	huma.Post(hapi, "/api/v1/orgs/{org}/triggers/{id}/rotate-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body triggerRotateRequestBody
	}) (*runRequestDetailOutput, error) {
		scope, id, err := scoped(ctx, in.Org, in.ID)
		if err != nil {
			return nil, err
		}
		return create(ctx, scope, runrequests.CreateInput{Kind: runrequests.KindTrigger, Reason: in.Body.Reason, ExpiresInMinutes: in.Body.ExpiresIn,
			Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerRotate, TriggerID: id}})
	}, created, withAudit("requestTriggerRotation", "run_request"))
}

// triggerTarget names what a trigger starts, for an audit summary.
func triggerTarget(s *runrequests.TriggerSpec) string {
	if s.Kind == triggers.KindRepoSync {
		return "repository sync of " + uuidString(s.RepoID)
	}
	return "run of job template " + uuidString(s.TemplateID)
}

// triggerRequestSummary is the audit summary of a trigger request: what it
// does to which trigger, the trigger's name and what it starts, and the
// reason. A trigger request never holds a secret, so neither does this.
func triggerRequestSummary(r runrequests.Request) string {
	ch := r.Trigger
	var summary string
	switch ch.Action {
	case runrequests.TriggerRotate:
		summary = "requested a new secret for webhook trigger " + uuidString(r.TargetID)
	default:
		summary = "requested to " + string(ch.Action) + " webhook trigger " + uuidString(r.TargetID)
	}
	if ch.Spec != nil {
		summary += " (" + strconv.Quote(ch.Spec.Name) + ", " + triggerTarget(ch.Spec) + ")"
	}
	return summary + ": " + r.Reason
}

// triggerApprovalSummary is the audit summary of a trigger request's
// approval; the secret it handed out is never part of it.
func triggerApprovalSummary(a runrequests.Approved) string {
	summary := "approved trigger request (" + string(a.Trigger.Action) + " webhook trigger " + a.Trigger.TriggerID
	if a.TemplateRevision != 0 {
		summary += "; template revision " + strconv.FormatInt(a.TemplateRevision, 10)
	}
	return summary + ")"
}

// auditAppliedTrigger records what an approved trigger request did to the
// trigger in the trigger's own feed, as the trigger routes would,
// attributed to the approver.
func auditAppliedTrigger(ctx context.Context, deps Deps, scope Scope, a runrequests.Approved) {
	pt := a.Trigger
	var action, verb, name string
	switch pt.Action {
	case runrequests.TriggerCreate:
		action, verb = "createTrigger", "created webhook trigger "
	case runrequests.TriggerUpdate:
		action, verb = "updateTrigger", "updated webhook trigger "
	default:
		action, verb = "rotateTriggerSecret", "rotated the secret of webhook trigger "
	}
	switch {
	case pt.New != nil:
		name = pt.New.Name
	case pt.Current != nil:
		name = pt.Current.Name
	}
	recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" "+action, "trigger", pt.TriggerID,
		strings.TrimSpace(verb+name)+" (approved request "+a.Request.ID.String()+")")
}
