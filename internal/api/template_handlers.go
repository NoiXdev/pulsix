package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// templateScopeDTO is the scope wire format on the API surface: the same
// JSON shape render.Scope uses (group_id/host_ids), with uuids as strings.
type templateScopeDTO struct {
	GroupID *string  `json:"group_id"`
	HostIDs []string `json:"host_ids"`
}

// templateScopeInput is the scope a client sends: both halves optional, so
// huma does not demand host_ids when only a group is named (templateScopeDTO,
// the read form, keeps both fields always present).
type templateScopeInput struct {
	GroupID *string  `json:"group_id,omitempty"`
	HostIDs []string `json:"host_ids,omitempty"`
}

func toScopeDTO(s templates.Scope) templateScopeDTO {
	dto := templateScopeDTO{GroupID: optionalString(s.GroupID), HostIDs: make([]string, len(s.HostIDs))}
	for i, id := range s.HostIDs {
		dto.HostIDs[i] = id.String()
	}
	return dto
}

// templateDTO carries a template's extra vars as the service masked them:
// sealed values, and non-empty values under secret-typed fields, read as
// {"$secret": "set"} — never a plaintext secret.
type templateDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PlaybookID string `json:"playbook_id"`
	// SharedTemplateID is set when this row is a projection of an
	// installation-owned definition (spec v0.2f decisions 1, 2; Task 6's
	// templates.Template.SharedTemplateID), absent for an ordinary,
	// organization-owned template. repoDTO.SharedRepoID's own doc comment,
	// same reasoning: the GUI uses it to mark the row and disable name,
	// playbook, limit, extra vars and their schema, which templates.
	// Service already refuses to change on a projection
	// (refuseSharedFieldChange) — this field only lets the UI say so
	// before the user tries and is refused.
	SharedTemplateID string           `json:"shared_template_id,omitempty"`
	Scope            templateScopeDTO `json:"scope"`
	Limit            string           `json:"limit"`
	ExtraVars        map[string]any   `json:"extra_vars"`
	ExtraVarsSchema  *schema.Schema   `json:"extra_vars_schema,omitempty"`
	CredentialID     string           `json:"credential_id,omitempty"`
	// VaultIdentities is this template's own labelled vault password set
	// (spec decision 1, migration 00059): what it adds on top of its
	// repository's base set, never the repository's own. 4b resolves the
	// two against each other on a run.
	VaultIdentities []vaultIdentityDTO `json:"vault_identities"`
	Revision        int64              `json:"revision" doc:"The template's revision; send it back as If-Match (quoted, like the ETag) on PUT"`
	VarsMode        string             `json:"vars_mode" enum:"off,manual,automatic" doc:"Whether the variables follow the playbook's variable sources"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// toTemplateVaultCredentialDTOs converts a template's own added vault
// credentials (templates.VaultCredential, no FromTemplate field — see that
// type's own comment) to the wire shape shared with repositories.
func toTemplateVaultCredentialDTOs(items []templates.VaultCredential) []vaultIdentityDTO {
	out := make([]vaultIdentityDTO, len(items))
	for i, it := range items {
		out[i] = vaultIdentityDTO{CredentialID: it.CredentialID.String(), Label: it.Label}
	}
	return out
}

// parseTemplateVaultCredentials mirrors parseVaultIdentities
// (repo_handlers.go) for the template's own vault credential store type.
func parseTemplateVaultCredentials(in []vaultIdentityDTO) ([]templates.VaultCredential, error) {
	out := make([]templates.VaultCredential, len(in))
	for i, v := range in {
		id, err := parseUUID(v.CredentialID, "vault identity credential id")
		if err != nil {
			return nil, err
		}
		out[i] = templates.VaultCredential{CredentialID: id, Label: v.Label}
	}
	return out, nil
}

func toTemplateDTO(t templates.Template) templateDTO {
	// PlaybookID is "" rather than the nil UUID's own string form
	// ("00000000-...") when t.PlaybookID is uuid.Nil — the unresolved case
	// Template.PlaybookID's own doc comment describes, possible only for a
	// projection whose organization has not synced a matching playbook yet.
	// An ordinary template's PlaybookID is never uuid.Nil (templates.
	// Service.build always resolves one through playbooks.Get first), so
	// this never changes that shape, only gives the GUI a field it can test
	// for emptiness instead of a string that merely looks like an id.
	playbookID := ""
	if t.PlaybookID != uuid.Nil {
		playbookID = t.PlaybookID.String()
	}
	dto := templateDTO{
		ID: t.ID.String(), Name: t.Name, PlaybookID: playbookID,
		Scope: toScopeDTO(t.Scope), Limit: t.Limit,
		ExtraVars: t.ExtraVars, ExtraVarsSchema: t.ExtraVarsSchema,
		VaultIdentities: toTemplateVaultCredentialDTOs(t.VaultCredentials),
		Revision:        t.Revision, VarsMode: string(t.VarsMode),
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if t.ExtraVars == nil {
		dto.ExtraVars = map[string]any{}
	}
	if t.CredentialID != nil {
		dto.CredentialID = t.CredentialID.String()
	}
	if t.SharedTemplateID != nil {
		dto.SharedTemplateID = t.SharedTemplateID.String()
	}
	return dto
}

type templateListInput struct {
	Org string `path:"org"`
}

type templateListOutput struct {
	Body struct {
		Templates []templateDTO `json:"templates"`
	}
}

type templateGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type templateDetailOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Template templateDTO `json:"template"`
	}
}

type TemplateBody struct {
	// Scope names the run's targets: group_id, host_ids or both; an empty
	// scope runs against every managed host of the organization.
	Scope           templateScopeInput `json:"scope,omitempty"`
	Limit           string             `json:"limit,omitempty" maxLength:"500" doc:"Free-form ansible limit pattern"`
	ExtraVars       map[string]any     `json:"extra_vars,omitempty" doc:"Validated against extra_vars_schema when one is set; secrets read back as {\"$secret\": \"set\"}"`
	ExtraVarsSchema *schema.Schema     `json:"extra_vars_schema,omitempty" doc:"The optional typed schema the extra vars must satisfy"`
	CredentialID    string             `json:"credential_id,omitempty" doc:"The default SSH credential for hosts without one of their own"`
	// VaultIdentities is this template's own added labelled vault passwords
	// (spec decision 1), on top of its repository's base set.
	VaultIdentities []vaultIdentityDTO `json:"vault_identities,omitempty"`
}

func (b TemplateBody) parseScope() (templates.Scope, error) {
	group, err := parseOptionalUUID(b.Scope.GroupID, "scope.group_id")
	if err != nil {
		return templates.Scope{}, err
	}
	hostIDs := make([]uuid.UUID, 0, len(b.Scope.HostIDs))
	for _, raw := range b.Scope.HostIDs {
		id, err := parseUUID(raw, "scope.host_ids")
		if err != nil {
			return templates.Scope{}, err
		}
		hostIDs = append(hostIDs, id)
	}
	return templates.Scope{GroupID: group, HostIDs: hostIDs}, nil
}

type templateCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		Name       string `json:"name" minLength:"1" maxLength:"63"`
		PlaybookID string `json:"playbook_id"`
		TemplateBody
	}
}

type templateUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The template's ETag; optional, a stale one answers 412"`
	Body    struct {
		Name       string `json:"name" minLength:"1" maxLength:"63"`
		PlaybookID string `json:"playbook_id"`
		TemplateBody
	}
}

// TemplateBody is the part of a create/update body both share. It is
// embedded and its type name starts uppercase on purpose — the inverse of
// credentialMaterial's lesson (see credential_handlers.go): huma's schema
// flattening only promotes an *exported* anonymous struct field, so an
// unexported one here would silently drop scope, limit, extra_vars,
// extra_vars_schema and credential_id from the request schema and reject
// them as unexpected properties. encoding/json promotes the embedded fields
// either way.
// registerTemplateRoutes wires the job-template registry: reads need
// templates.read (viewer), writes templates.write (operator). Responses
// carry extra vars with secret values masked; never a plaintext secret.
func registerTemplateRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/job-templates", func(ctx context.Context, in *templateListInput) (*templateListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Templates.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		out := &templateListOutput{}
		out.Body.Templates = make([]templateDTO, len(list))
		for i, t := range list {
			out.Body.Templates[i] = toTemplateDTO(t)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/job-templates", func(ctx context.Context, in *templateCreateInput) (*templateDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		playbookID, err := parseUUID(in.Body.PlaybookID, "playbook id")
		if err != nil {
			return nil, err
		}
		sc, err := in.Body.parseScope()
		if err != nil {
			return nil, err
		}
		credentialID, err := optionalUUID(in.Body.CredentialID, "credential id")
		if err != nil {
			return nil, err
		}
		vaultCredentials, err := parseTemplateVaultCredentials(in.Body.VaultIdentities)
		if err != nil {
			return nil, err
		}
		t, err := deps.Templates.Create(ctx, scope.OrgID, templates.CreateInput{
			Name: in.Body.Name, PlaybookID: playbookID, Scope: sc, Limit: in.Body.Limit,
			ExtraVars: in.Body.ExtraVars, ExtraVarsSchema: in.Body.ExtraVarsSchema, CredentialID: credentialID,
			VaultCredentials: vaultCredentials,
		})
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "created job template "+t.Name)
		return templateDetail(t), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createTemplate", "job_template"))

	huma.Get(hapi, "/api/v1/orgs/{org}/job-templates/{id}", func(ctx context.Context, in *templateGetInput) (*templateDetailOutput, error) {
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
		return templateDetail(t), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/job-templates/{id}", func(ctx context.Context, in *templateUpdateInput) (*templateDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		// Unlike Create's own required playbook_id (parseUUID below),
		// Update must also accept "" here: a projected template's own GET
		// renders an unresolved playbook_id as "" (toTemplateDTO's own
		// comment), and the GUI echoes exactly that back on a save that
		// only touches the projection's own fields (scope, credential) —
		// templates.Service.Update's own refuseSharedFieldChange is what
		// actually enforces that this value may not CHANGE, not this
		// parse step.
		playbookID := uuid.Nil
		if in.Body.PlaybookID != "" {
			if playbookID, err = parseUUID(in.Body.PlaybookID, "playbook id"); err != nil {
				return nil, err
			}
		}
		sc, err := in.Body.parseScope()
		if err != nil {
			return nil, err
		}
		credentialID, err := optionalUUID(in.Body.CredentialID, "credential id")
		if err != nil {
			return nil, err
		}
		vaultCredentials, err := parseTemplateVaultCredentials(in.Body.VaultIdentities)
		if err != nil {
			return nil, err
		}
		ifRevision, err := optionalIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		t, err := deps.Templates.Update(ctx, scope.OrgID, id, templates.UpdateInput{
			Name: in.Body.Name, PlaybookID: playbookID, Scope: sc, Limit: in.Body.Limit,
			ExtraVars: in.Body.ExtraVars, ExtraVarsSchema: in.Body.ExtraVarsSchema, CredentialID: credentialID,
			VaultCredentials: vaultCredentials, IfRevision: ifRevision,
		})
		if err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "updated job template "+t.Name)
		return templateDetail(t), nil
	}, withAudit("updateTemplate", "job_template"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/job-templates/{id}", func(ctx context.Context, in *templateGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		if err := deps.Templates.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapTemplatesErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted job template")
		return &okOutput{}, nil
	}, withAudit("deleteTemplate", "job_template"))
}

func templateDetail(t templates.Template) *templateDetailOutput {
	out := &templateDetailOutput{ETag: revisionETag(t.Revision)}
	out.Body.Template = toTemplateDTO(t)
	return out
}

// mapTemplatesErr converts templates.Service errors to HTTP. A
// *schema.ValidationError becomes 422 with one detail per problem, located
// under body.extra_vars (values failing the schema) or
// body.extra_vars_schema (the schema document itself malformed), so the SPA
// can show each message at its field.
func mapTemplatesErr(ctx context.Context, err error) error {
	var ve *schema.ValidationError
	if errors.As(err, &ve) {
		location := "body.extra_vars"
		if errors.Is(err, templates.ErrBadSchema) {
			location = "body.extra_vars_schema"
		}
		details := make([]error, len(ve.Problems))
		for i, p := range ve.Problems {
			loc := location
			if p.Path != "" {
				loc += "." + p.Path
			}
			details[i] = &huma.ErrorDetail{Location: loc, Message: p.Message}
		}
		return huma.Error422UnprocessableEntity("validation failed", details...)
	}
	switch {
	case errors.Is(err, templates.ErrUnmanagedHost):
		return huma.Error422UnprocessableEntity("only managed hosts can be targeted by a job template")
	case errors.Is(err, templates.ErrNotSSH):
		return huma.Error422UnprocessableEntity("the default credential must be an SSH key or password credential")
	case errors.Is(err, templates.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, templates.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, templates.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, templates.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	case errors.Is(err, templates.ErrForbidden):
		// spec v0.2f decision 6: a projected template cannot be deleted
		// from inside an organization. repos' own mapReposErr gives
		// repos.ErrForbidden the same mapping, for the same reason: this is
		// not a value the caller could fix, so 403, not 400.
		return huma.Error403Forbidden(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped templates error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
