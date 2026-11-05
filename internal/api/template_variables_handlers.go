package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// templateVariablesDTO is a template's variables page: its schema, its
// values as the service masked them (secrets read as {"$secret": "set"}),
// its revision and whether the variables follow the playbook's sources.
type templateVariablesDTO struct {
	ExtraVarsSchema *schema.Schema `json:"extra_vars_schema,omitempty"`
	ExtraVars       map[string]any `json:"extra_vars"`
	Revision        int64          `json:"revision" doc:"The template's revision; send it back as If-Match (quoted, like the ETag) on PUT"`
	Mode            string         `json:"mode" enum:"off,manual,automatic" doc:"Whether the variables follow the playbook's variable sources"`
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
		t, err := deps.Templates.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		return newTemplateVariablesOutput(t), nil
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
}
