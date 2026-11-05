package api

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/varsync"
)

// templateVariablesDTO is a template's variables page: its schema, its
// values as the service masked them (secrets read as {"$secret": "set"}),
// its revision and whether the variables follow the playbook's sources.
// The read adds the playbook's variable sources and what is left to
// review; a write's answer leaves them out (read the page again).
type templateVariablesDTO struct {
	ExtraVarsSchema *schema.Schema              `json:"extra_vars_schema,omitempty"`
	ExtraVars       map[string]any              `json:"extra_vars"`
	Revision        int64                       `json:"revision" doc:"The template's revision; send it back as If-Match (quoted, like the ETag) on a write"`
	Mode            string                      `json:"mode" enum:"off,manual,automatic" doc:"Whether the variables follow the playbook's variable sources"`
	Source          *templateVariablesSourceDTO `json:"source,omitempty" doc:"The playbook's variable sources as last read (the read only)"`
	Review          *templateVariablesReviewDTO `json:"review,omitempty" doc:"What the variables leave for a person to review (the read only)"`
}

// templateVariablesSourceDTO is a template's playbook's variable sources
// as last read (templates.SourceInfo).
type templateVariablesSourceDTO struct {
	Kind        string     `json:"kind" doc:"repo (read by the repository's last sync) or inline (read from the editor playbook's text); empty without a playbook"`
	Commit      string     `json:"commit" doc:"The revision read: the commit, or text:<hash> for an editor playbook; empty when unknown"`
	SyncedAt    *time.Time `json:"synced_at,omitempty" doc:"When the repository's sync recorded the read"`
	AppliedRev  string     `json:"applied_rev" doc:"The revision the template last applied"`
	Newer       bool       `json:"newer" doc:"A usable read the template has not applied yet"`
	WorkerReads bool       `json:"worker_reads" doc:"False when the worker that synced the repository does not read variable sources"`
	Incomplete  bool       `json:"incomplete" doc:"A limit left playbooks of the repository unread"`
	Found       bool       `json:"found" doc:"A read of this playbook exists"`
	LeftOut     bool       `json:"left_out" doc:"The last sync left this playbook out (a limit was reached)"`
	Available   bool       `json:"available" doc:"The read may change the template; reason says why not"`
	Reason      string     `json:"reason,omitempty"`
	RepoID      string     `json:"repo_id,omitempty"`
	Path        string     `json:"path,omitempty"`
}

// templateVariablesReviewDTO is what a template's variables leave for a
// person (templates.Review).
type templateVariablesReviewDTO struct {
	Gone            int `json:"gone" doc:"Fields gone from the source; removing them is confirmed in an apply"`
	NeedsReview     int `json:"needs_review" doc:"Source changes held for a person (a type or secret change, or a stored value that no longer fits)"`
	MissingRequired int `json:"missing_required" doc:"Active required fields without a default or a value"`
	LeftOut         int `json:"left_out" doc:"Source fields not added because a template holds at most 100 fields"`
}

func toSourceDTO(si templates.SourceInfo) *templateVariablesSourceDTO {
	dto := &templateVariablesSourceDTO{
		Kind: si.Kind, Commit: si.Rev, SyncedAt: si.SyncedAt, AppliedRev: si.AppliedRev, Newer: si.Newer,
		WorkerReads: si.WorkerReads, Incomplete: si.Incomplete, Found: si.Found, LeftOut: si.LeftOut,
		Available: si.Available, Reason: si.Reason, Path: si.Path,
	}
	if si.RepoID != nil {
		dto.RepoID = si.RepoID.String()
	}
	return dto
}

func toReviewDTO(r templates.Review) *templateVariablesReviewDTO {
	return &templateVariablesReviewDTO{Gone: r.Gone, NeedsReview: r.NeedsReview, MissingRequired: r.MissingRequired, LeftOut: r.LeftOut}
}

type templateVariablesModeInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The template's ETag; optional, a stale one answers 412"`
	Body    struct {
		Mode string `json:"mode" enum:"off,manual,automatic" doc:"manual and automatic switch a linked template; off unlinks it, every field turning manual. Linking goes through the preview and the apply"`
	}
}

type templateVariablesPreviewOutput struct {
	Body struct {
		Preview varsync.Preview            `json:"preview"`
		Source  templateVariablesSourceDTO `json:"source"`
	}
}

type templateVariablesApplyInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The template's ETag; optional, a stale one answers 412"`
	Body    struct {
		Mode       string         `json:"mode" enum:"manual,automatic" doc:"The mode the variables are in afterwards"`
		SourceRev  string         `json:"source_rev" doc:"The preview's source_rev; another revision than the current read answers 409"`
		RemoveGone bool           `json:"remove_gone,omitempty" doc:"Also remove the fields gone from the source (their values stay stored)"`
		Values     map[string]any `json:"values,omitempty" doc:"Values entered over the stored ones: a value that no longer fits its field, a new required field's value; null removes a stored value"`
	}
}

type templateVariablesOutput struct {
	ETag string `header:"ETag"`
	Body templateVariablesDTO
}

type templateVariablesUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The template's ETag; optional, a stale one answers 412"`
	Body    struct {
		ExtraVarsSchema *schema.Schema `json:"extra_vars_schema,omitempty" doc:"The schema; omit to remove it, which also drops every value kept for an inactive or removed field"`
		ExtraVars       map[string]any `json:"extra_vars,omitempty" doc:"The values of the active fields. Values of inactive and removed fields the body omits stay stored. Secrets: a string sets a new value, the {\"$secret\": \"set\"} a read returned keeps the stored one"`
	}
}

func newTemplateVariablesOutput(t templates.Template) *templateVariablesOutput {
	out := &templateVariablesOutput{ETag: revisionETag(t.Revision)}
	out.Body = templateVariablesDTO{
		ExtraVarsSchema: t.ExtraVarsSchema, ExtraVars: t.ExtraVars,
		Revision: t.Revision, Mode: string(t.VarsMode),
	}
	if out.Body.ExtraVars == nil {
		out.Body.ExtraVars = map[string]any{}
	}
	return out
}

// templateVariablesPutDoc describes the variables write for API clients.
const templateVariablesPutDoc = "Replaces the template's extra vars schema and values. " +
	"The value of a field that is inactive, or that the schema no longer declares, is kept when the body omits it, and is not sent to runs. " +
	"To discard such a value, declare the field active and omit its value; remove the field again in a later write if wanted. " +
	"A body without extra_vars_schema removes the schema and with it every kept value: the values are then exactly the body's."

// registerTemplateVarsRoutes wires a template's variables page (backlog
// 7.75): reading them needs templates.read, writing them templates.write.
// Values are masked on every response; never a plaintext secret.
func registerTemplateVarsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/job-templates/{id}/variables", func(ctx context.Context, in *templateGetInput) (*templateVariablesOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		view, err := deps.Templates.Variables(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		out := newTemplateVariablesOutput(view.Template)
		out.Body.Source, out.Body.Review = toSourceDTO(view.Source), toReviewDTO(view.Review)
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/job-templates/{id}/variables", func(ctx context.Context, in *templateVariablesUpdateInput) (*templateVariablesOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		ifRevision, err := optionalIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		t, err := deps.Templates.UpdateVariables(ctx, scope.OrgID, id, templates.VariablesInput{
			Schema: in.Body.ExtraVarsSchema, ExtraVars: in.Body.ExtraVars, IfRevision: ifRevision,
		})
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "updated the variables of job template "+t.Name)
		return newTemplateVariablesOutput(t), nil
	}, withAudit("updateTemplateVariables", "job_template"), func(o *huma.Operation) {
		o.Description = templateVariablesPutDoc
	})

	registerTemplateVarsLinkRoutes(hapi, deps)
}

// registerTemplateVarsLinkRoutes wires the link of a template's variables
// to its playbook's variable sources: the mode, the preview ("Sync now")
// and the apply, all templates.write.
func registerTemplateVarsLinkRoutes(hapi huma.API, deps Deps) {
	huma.Put(hapi, "/api/v1/orgs/{org}/job-templates/{id}/variables/mode", func(ctx context.Context, in *templateVariablesModeInput) (*templateVariablesOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		ifRevision, err := optionalIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		t, err := deps.Templates.SetVariablesMode(ctx, scope.OrgID, id, templates.SourceMode(in.Body.Mode), ifRevision)
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "set the variables of job template "+t.Name+" to mode "+in.Body.Mode)
		return newTemplateVariablesOutput(t), nil
	}, withAudit("setTemplateVariablesMode", "job_template"))

	huma.Post(hapi, "/api/v1/orgs/{org}/job-templates/{id}/variables/preview", func(ctx context.Context, in *templateGetInput) (*templateVariablesPreviewOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		p, si, err := deps.Templates.PreviewVariables(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		out := &templateVariablesPreviewOutput{}
		out.Body.Preview, out.Body.Source = p, *toSourceDTO(si)
		return out, nil
	}, func(o *huma.Operation) {
		o.Description = "Compares the template's fields with its playbook's variable sources as last read and lists what an apply would change. Saves nothing."
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/job-templates/{id}/variables/apply", func(ctx context.Context, in *templateVariablesApplyInput) (*templateVariablesOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		ifRevision, err := optionalIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		t, err := deps.Templates.ApplyVariables(ctx, scope.OrgID, id, templates.ApplyInput{
			Mode: templates.SourceMode(in.Body.Mode), SourceRev: in.Body.SourceRev, RemoveGone: in.Body.RemoveGone,
			Values: in.Body.Values, IfRevision: ifRevision,
		})
		if err != nil {
			return nil, mapTemplateApplyErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "applied the playbook's variables to job template "+t.Name)
		return newTemplateVariablesOutput(t), nil
	}, withAudit("applyTemplateVariables", "job_template"), func(o *huma.Operation) {
		o.Description = "Applies the preview of the playbook's variable sources: it is computed anew, and a source_rev other than the current read's answers 409. " +
			"The values are checked like a save; a value that no longer fits its field is entered under values."
	})
}

// mapTemplateApplyErr maps an apply's errors: a stale preview is 409, a
// value problem is located under body.values; the rest as every template
// error.
func mapTemplateApplyErr(ctx context.Context, err error) error {
	if errors.Is(err, templates.ErrSourceChanged) {
		return huma.Error409Conflict("the source changed since the preview; preview again")
	}
	var ve *schema.ValidationError
	if errors.As(err, &ve) && !errors.Is(err, templates.ErrBadSchema) {
		details := make([]error, len(ve.Problems))
		for i, p := range ve.Problems {
			details[i] = &huma.ErrorDetail{Location: "body.values." + p.Path, Message: p.Message}
		}
		return huma.Error422UnprocessableEntity("validation failed", details...)
	}
	return mapTemplatesErr(ctx, err)
}
