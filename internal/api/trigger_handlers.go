package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
)

type triggerListInput struct {
	Org        string `path:"org"`
	TemplateID string `query:"template_id"`
	RepoID     string `query:"repo_id"`
}

type triggerListOutput struct {
	Body struct {
		Triggers []triggerDTO `json:"triggers"`
	}
}

type triggerIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type triggerCreateInput struct {
	Org  string `path:"org"`
	Body TriggerBody
}

type triggerUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The trigger's ETag; required"`
	Body    TriggerBody
}

type triggerOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Trigger triggerDTO `json:"trigger"`
	}
}

type triggerSecretOutput struct {
	Body struct {
		Trigger triggerDTO `json:"trigger"`
		Secret  string     `json:"secret" doc:"Shown once; store it in the sender"`
		URL     string     `json:"url"`
	}
}

type triggerTestInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Payload map[string]any `json:"payload,omitempty" doc:"JSON body to send; default {}"`
	}
}

type triggerTestOutput struct {
	Body struct {
		Outcome string `json:"outcome"`
		RunID   string `json:"run_id,omitempty"`
	}
}

type triggerDeliveriesInput struct {
	Org   string `path:"org"`
	ID    string `path:"id"`
	Limit int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
}

type triggerDeliveriesOutput struct {
	Body struct {
		Deliveries []triggerDeliveryDTO `json:"deliveries"`
	}
}

// triggerPerm is the permission a trigger's kind needs.
func triggerPerm(kind triggers.Kind, write bool) tenancy.Permission {
	switch {
	case kind == triggers.KindRepoSync && write:
		return tenancy.PermReposWrite
	case kind == triggers.KindRepoSync:
		return tenancy.PermReposRead
	case write:
		return tenancy.PermTemplatesWrite
	}
	return tenancy.PermTemplatesRead
}

// loadTrigger resolves the org (viewer level), loads the trigger and then
// requires the permission its kind needs.
func loadTrigger(ctx context.Context, deps Deps, org, rawID string, write bool) (Scope, triggers.Trigger, error) {
	scope, err := requireOrg(ctx, deps, org, tenancy.PermTemplatesRead)
	if err != nil {
		return Scope{}, triggers.Trigger{}, err
	}
	id, err := parseUUID(rawID, "trigger id")
	if err != nil {
		return Scope{}, triggers.Trigger{}, err
	}
	t, err := deps.Triggers.Get(ctx, scope.OrgID, id)
	if err != nil {
		return Scope{}, triggers.Trigger{}, mapTriggersErr(ctx, err)
	}
	if !tenancy.Allows(scope.Role, triggerPerm(t.Kind, write)) {
		return Scope{}, triggers.Trigger{}, huma.Error403Forbidden("insufficient role")
	}
	return scope, t, nil
}

// registerTriggerRoutes wires spec §3's incoming webhook triggers:
// templates.read/write for template_run triggers, repos.read/write for
// repo_sync triggers, plus jobs.run for a template_run test call.
func registerTriggerRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/triggers", func(ctx context.Context, in *triggerListInput) (*triggerListOutput, error) {
		// The permission requireOrg checks depends on the filter, so the
		// filter is read first; the session's own gates run ahead of it, so
		// a caller who may not be here at all (a pending second factor, a
		// forced password change) is refused as such rather than told
		// about its query.
		if _, err := requireMFAComplete(ctx); err != nil {
			return nil, err
		}
		var filter triggers.ListFilter
		var perm tenancy.Permission
		switch {
		case in.TemplateID != "" && in.RepoID == "":
			perm = tenancy.PermTemplatesRead
		case in.RepoID != "" && in.TemplateID == "":
			perm = tenancy.PermReposRead
		default:
			return nil, huma.Error400BadRequest("pass exactly one of template_id or repo_id")
		}
		scope, err := requireOrg(ctx, deps, in.Org, perm)
		if err != nil {
			return nil, err
		}
		if filter.TemplateID, err = optionalUUID(in.TemplateID, "template id"); err != nil {
			return nil, err
		}
		if filter.RepoID, err = optionalUUID(in.RepoID, "repo id"); err != nil {
			return nil, err
		}
		list, err := deps.Triggers.List(ctx, scope.OrgID, filter)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		out := &triggerListOutput{}
		out.Body.Triggers = make([]triggerDTO, len(list))
		for i, t := range list {
			out.Body.Triggers[i] = toTriggerDTO(t, deps.Triggers.URL(t))
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/triggers", func(ctx context.Context, in *triggerCreateInput) (*triggerSecretOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, triggerPerm(triggers.Kind(in.Body.Kind), true))
		if err != nil {
			return nil, err
		}
		input, err := triggerInput(in.Body)
		if err != nil {
			return nil, err
		}
		c, err := deps.Triggers.Create(ctx, scope.OrgID, scope.UserID, input)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, c.Trigger.ID.String(), "created webhook trigger "+c.Trigger.Name)
		return triggerSecret(c), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createTrigger", "trigger"))

	huma.Get(hapi, "/api/v1/orgs/{org}/triggers/{id}", func(ctx context.Context, in *triggerIDInput) (*triggerOutput, error) {
		_, t, err := loadTrigger(ctx, deps, in.Org, in.ID, false)
		if err != nil {
			return nil, err
		}
		return triggerOut(deps, t), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/triggers/{id}", func(ctx context.Context, in *triggerUpdateInput) (*triggerOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, true)
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		input, err := triggerInput(in.Body)
		if err != nil {
			return nil, err
		}
		up, err := deps.Triggers.Update(ctx, scope.OrgID, t.ID, rev, input)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, up.ID.String(), "updated webhook trigger "+up.Name)
		return triggerOut(deps, up), nil
	}, withAudit("updateTrigger", "trigger"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/triggers/{id}", func(ctx context.Context, in *triggerIDInput) (*okOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, true)
		if err != nil {
			return nil, err
		}
		if err := deps.Triggers.Delete(ctx, scope.OrgID, t.ID); err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "deleted webhook trigger "+t.Name)
		return &okOutput{}, nil
	}, withAudit("deleteTrigger", "trigger"))

	huma.Post(hapi, "/api/v1/orgs/{org}/triggers/{id}/rotate-secret", func(ctx context.Context, in *triggerIDInput) (*triggerSecretOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, true)
		if err != nil {
			return nil, err
		}
		c, err := deps.Triggers.RotateSecret(ctx, scope.OrgID, t.ID)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "rotated the secret of webhook trigger "+t.Name)
		return triggerSecret(c), nil
	}, withAudit("rotateTriggerSecret", "trigger"))

	huma.Post(hapi, "/api/v1/orgs/{org}/triggers/{id}/test", func(ctx context.Context, in *triggerTestInput) (*triggerTestOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, true)
		if err != nil {
			return nil, err
		}
		if t.Kind == triggers.KindTemplateRun && !tenancy.Allows(scope.Role, tenancy.PermJobsRun) {
			return nil, huma.Error403Forbidden("insufficient role")
		}
		var body []byte
		if in.Body.Payload != nil {
			if body, err = json.Marshal(in.Body.Payload); err != nil {
				return nil, huma.Error400BadRequest("payload is not JSON")
			}
		}
		res, err := deps.Triggers.TestCall(ctx, scope.OrgID, t.ID, body)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "test call of webhook trigger "+t.Name+": "+string(res.Outcome))
		out := &triggerTestOutput{}
		out.Body.Outcome = string(res.Outcome)
		if res.RunID != nil {
			out.Body.RunID = res.RunID.String()
		}
		return out, nil
	}, withAudit("testTrigger", "trigger"))

	huma.Get(hapi, "/api/v1/orgs/{org}/triggers/{id}/deliveries", func(ctx context.Context, in *triggerDeliveriesInput) (*triggerDeliveriesOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, false)
		if err != nil {
			return nil, err
		}
		list, err := deps.Triggers.Deliveries(ctx, scope.OrgID, t.ID, in.Limit)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		out := &triggerDeliveriesOutput{}
		out.Body.Deliveries = make([]triggerDeliveryDTO, len(list))
		for i, d := range list {
			out.Body.Deliveries[i] = toTriggerDeliveryDTO(d)
		}
		return out, nil
	})
}

func triggerInput(b TriggerBody) (triggers.Input, error) {
	in := triggers.Input{
		Kind: triggers.Kind(b.Kind), Name: b.Name, Enabled: b.Enabled == nil || *b.Enabled, Preset: triggers.Preset(b.Preset),
		TimestampHeader: b.TimestampHeader, AllowCIDRs: b.AllowCIDRs, OnActive: triggers.OnActive(b.OnActive),
	}
	var err error
	if in.TemplateID, err = optionalUUID(b.TemplateID, "template id"); err != nil {
		return in, err
	}
	if in.RepoID, err = optionalUUID(b.RepoID, "repo id"); err != nil {
		return in, err
	}
	if b.Mapping != nil {
		in.Mapping = triggers.Mapping{ExtraVars: b.Mapping.ExtraVars, Limit: b.Mapping.Limit}
	}
	if b.RateLimit != nil {
		in.RateLimit = triggers.RateLimit{Count: b.RateLimit.Count, Per: b.RateLimit.Per}
	}
	return in, nil
}

func triggerOut(deps Deps, t triggers.Trigger) *triggerOutput {
	out := &triggerOutput{ETag: revisionETag(t.Revision)}
	out.Body.Trigger = toTriggerDTO(t, deps.Triggers.URL(t))
	return out
}

func triggerSecret(c triggers.Created) *triggerSecretOutput {
	out := &triggerSecretOutput{}
	out.Body.Trigger, out.Body.Secret, out.Body.URL = toTriggerDTO(c.Trigger, c.URL), c.Secret, c.URL
	return out
}

func mapTriggersErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, triggers.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, triggers.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, triggers.ErrNameTaken):
		return huma.Error409Conflict("a trigger with this name already exists")
	case errors.Is(err, triggers.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	}
	slog.ErrorContext(ctx, "unmapped triggers error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
