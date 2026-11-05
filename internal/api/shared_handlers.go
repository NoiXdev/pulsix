package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/shared"
)

// repoDefinitionDTO is the installation-owned half of a shared repository
// (spec v0.2f decision 2): name, url, branch and subpath, the fields every
// projection's own repoDTO (repo_handlers.go) echoes read-only once
// SharedRepoID is set. No credential, no vault identity — those are the
// projection's own, per spec decision 3.
type repoDefinitionDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	Subpath string `json:"subpath"`
}

func toRepoDefinitionDTO(d shared.RepoDefinition) repoDefinitionDTO {
	return repoDefinitionDTO{ID: d.ID.String(), Name: d.Name, URL: d.URL, Branch: d.Branch, Subpath: d.Subpath}
}

type repoDefinitionListOutput struct {
	Body struct {
		Definitions []repoDefinitionDTO `json:"definitions"`
	}
}

type repoDefinitionGetInput struct {
	ID string `path:"id"`
}

type repoDefinitionDetailOutput struct {
	Body struct {
		Definition repoDefinitionDTO `json:"definition"`
	}
}

// repoDefinitionBody is the editable shape both create and update take —
// the same full-replacement convention repoCreateInput/repoUpdateInput
// already use for an ordinary repository, and the same length bounds
// (shared.RepoDefinitionInput itself enforces none: Task 2's own report
// reads the absence of ErrInvalidInput from its sentinel list as a
// deliberate signal that format validation belongs at this schema layer).
type repoDefinitionBody struct {
	Name    string `json:"name" minLength:"1" maxLength:"63"`
	URL     string `json:"url" minLength:"1" maxLength:"500"`
	Branch  string `json:"branch,omitempty" maxLength:"100"`
	Subpath string `json:"subpath,omitempty" maxLength:"200"`
}

type repoDefinitionCreateInput struct {
	Body repoDefinitionBody
}

type repoDefinitionUpdateInput struct {
	ID   string `path:"id"`
	Body repoDefinitionBody
}

func repoDefinitionDetail(d shared.RepoDefinition) *repoDefinitionDetailOutput {
	out := &repoDefinitionDetailOutput{}
	out.Body.Definition = toRepoDefinitionDTO(d)
	return out
}

// registerSharedRepoRoutes wires the installation admin's own CRUD over
// repository definitions (spec v0.2f decision 8: no org_id, no per-org
// access at all — access is requireInstanceAdmin, the same gate
// registerSMTPSettingsRoutes and registerAdminRoutes already use for their
// own instance-level routes, and for the same reason: a bearer token has no
// per-org scope to narrow here, so it must never reach these routes
// regardless of the privilege its owner holds elsewhere).
//
// Every verb gets its own requireInstanceAdmin call rather than a group-
// level middleware, matching every other instance-level route file in this
// package (registerAdminRoutes, registerSMTPSettingsRoutes) — see the
// per-verb 403 test (TestRepoDefinitionRoutesRequireInstanceAdmin) that
// exists specifically because a group-level gate is easy to apply to only
// the first route registered under it and easy to forget on the rest.
//
// Registered on the root API (api.go's registerRoutes), not the
// bearer-enabled orgScoped group: these routes take no {org} in their path
// and requireInstanceAdmin itself (via requireInteractiveSession) already
// refuses a bearer token outright, the same reasoning
// registerAdminRoutes' own doc comment gives.
func registerSharedRepoRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/repos/definitions", func(ctx context.Context, _ *struct{}) (*repoDefinitionListOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		list, err := deps.Shared.ListRepos(ctx)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		out := &repoDefinitionListOutput{}
		out.Body.Definitions = make([]repoDefinitionDTO, len(list))
		for i, d := range list {
			out.Body.Definitions[i] = toRepoDefinitionDTO(d)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/admin/repos/definitions", func(ctx context.Context, in *repoDefinitionCreateInput) (*repoDefinitionDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.CreateRepo(ctx, shared.RepoDefinitionInput{
			Name: in.Body.Name, URL: in.Body.URL, Branch: in.Body.Branch, Subpath: in.Body.Subpath,
		})
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		// No requireOrg here — there is no organization to resolve one
		// from, the same reasoning registerAdminRoutes/
		// registerSMTPSettingsRoutes give their own instance-level writes —
		// so the carrier's scope is filled in directly, with a nil org and
		// the instance admin as the actor.
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "created shared repository definition "+d.Name)
		return repoDefinitionDetail(d), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createRepoDefinition", "shared_repo"))

	huma.Get(hapi, "/api/v1/admin/repos/definitions/{id}", func(ctx context.Context, in *repoDefinitionGetInput) (*repoDefinitionDetailOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.GetRepo(ctx, id)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		return repoDefinitionDetail(d), nil
	})

	huma.Put(hapi, "/api/v1/admin/repos/definitions/{id}", func(ctx context.Context, in *repoDefinitionUpdateInput) (*repoDefinitionDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.UpdateRepo(ctx, id, shared.RepoDefinitionInput{
			Name: in.Body.Name, URL: in.Body.URL, Branch: in.Body.Branch, Subpath: in.Body.Subpath,
		})
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "updated shared repository definition "+d.Name)
		return repoDefinitionDetail(d), nil
	}, withAudit("updateRepoDefinition", "shared_repo"))

	huma.Delete(hapi, "/api/v1/admin/repos/definitions/{id}", func(ctx context.Context, in *repoDefinitionGetInput) (*okOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo definition id")
		if err != nil {
			return nil, err
		}
		if err := deps.Shared.DeleteRepo(ctx, id); err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "deleted shared repository definition")
		return &okOutput{}, nil
	}, withAudit("deleteRepoDefinition", "shared_repo"))
}

// --- job templates (Task 6) ---
//
// The same shape as the repository definition DTOs and routes above, one
// kind reusing the same instance-admin mechanism (requireInstanceAdmin,
// mapSharedErr, resolveOrgLabel/resolveOrgLabels below) rather than a
// second, parallel set of helpers — tx.go's own comment is why the
// underlying shared.Store and shared.Service are one of each, not two;
// this file's own handler layer follows the same rule.

// templateDefinitionDTO is the installation-owned half of a shared job
// template (spec v0.2f decision 2): name, the shared repository it
// resolves its playbook against, the playbook's path within that
// repository, limit, extra vars and their schema — every projection's own
// templateDTO (template_handlers.go) echoes these read-only once
// SharedTemplateID is set. No scope, no credential — those are the
// projection's own.
type templateDefinitionDTO struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	SharedRepoID    string         `json:"shared_repo_id"`
	PlaybookPath    string         `json:"playbook_path"`
	Limit           string         `json:"limit"`
	ExtraVars       map[string]any `json:"extra_vars"`
	ExtraVarsSchema *schema.Schema `json:"extra_vars_schema,omitempty"`
}

func toTemplateDefinitionDTO(d shared.TemplateDefinition) templateDefinitionDTO {
	dto := templateDefinitionDTO{
		ID: d.ID.String(), Name: d.Name, SharedRepoID: d.SharedRepoID.String(), PlaybookPath: d.PlaybookPath,
		Limit: d.Limit, ExtraVars: d.ExtraVars, ExtraVarsSchema: d.ExtraVarsSchema,
	}
	if dto.ExtraVars == nil {
		dto.ExtraVars = map[string]any{}
	}
	return dto
}

type templateDefinitionListOutput struct {
	Body struct {
		Definitions []templateDefinitionDTO `json:"definitions"`
	}
}

type templateDefinitionGetInput struct {
	ID string `path:"id"`
}

type templateDefinitionDetailOutput struct {
	Body struct {
		Definition templateDefinitionDTO `json:"definition"`
	}
}

// templateDefinitionBody is the editable shape both create and update take
// — repoDefinitionBody's own doc comment, same convention: shared.
// TemplateDefinitionInput itself enforces no format rules on name or
// shared_repo_id, by design (format validation belongs at this schema
// layer). playbook_path is checked by shared.Service against the canonical
// repo playbook path rule (shared.ErrInvalidPlaybookPath, 422), and
// ExtraVars and ExtraVarsSchema are validated there too (shared.ErrInvalidInput):
// a well-formed schema with no secret-typed field and no field taken from
// a playbook, and no forbidden connection-security variable — checks this
// package cannot push down to a schema tag.
type templateDefinitionBody struct {
	Name            string         `json:"name" minLength:"1" maxLength:"63"`
	SharedRepoID    string         `json:"shared_repo_id"`
	PlaybookPath    string         `json:"playbook_path" minLength:"1" maxLength:"500"`
	Limit           string         `json:"limit,omitempty" maxLength:"500"`
	ExtraVars       map[string]any `json:"extra_vars,omitempty"`
	ExtraVarsSchema *schema.Schema `json:"extra_vars_schema,omitempty"`
}

type templateDefinitionCreateInput struct {
	Body templateDefinitionBody
}

type templateDefinitionUpdateInput struct {
	ID   string `path:"id"`
	Body templateDefinitionBody
}

func templateDefinitionDetail(d shared.TemplateDefinition) *templateDefinitionDetailOutput {
	out := &templateDefinitionDetailOutput{}
	out.Body.Definition = toTemplateDefinitionDTO(d)
	return out
}

// registerSharedTemplateRoutes wires the installation admin's own CRUD over
// job template definitions — registerSharedRepoRoutes' own doc comment,
// same gate, same per-verb 403 reasoning
// (TestTemplateDefinitionRoutesRequireInstanceAdmin), same root-API
// registration (no {org} in the path, no bearer token admitted).
func registerSharedTemplateRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/templates/definitions", func(ctx context.Context, _ *struct{}) (*templateDefinitionListOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		list, err := deps.Shared.ListTemplateDefs(ctx)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		out := &templateDefinitionListOutput{}
		out.Body.Definitions = make([]templateDefinitionDTO, len(list))
		for i, d := range list {
			out.Body.Definitions[i] = toTemplateDefinitionDTO(d)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/admin/templates/definitions", func(ctx context.Context, in *templateDefinitionCreateInput) (*templateDefinitionDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		repoID, err := parseUUID(in.Body.SharedRepoID, "shared repo id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.CreateTemplate(ctx, shared.TemplateDefinitionInput{
			Name: in.Body.Name, SharedRepoID: repoID, PlaybookPath: in.Body.PlaybookPath,
			Limit: in.Body.Limit, ExtraVars: in.Body.ExtraVars, ExtraVarsSchema: in.Body.ExtraVarsSchema,
		})
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "created shared job template definition "+d.Name)
		return templateDefinitionDetail(d), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createTemplateDefinition", "shared_job_template"))

	huma.Get(hapi, "/api/v1/admin/templates/definitions/{id}", func(ctx context.Context, in *templateDefinitionGetInput) (*templateDefinitionDetailOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.GetTemplateDef(ctx, id)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		return templateDefinitionDetail(d), nil
	})

	huma.Put(hapi, "/api/v1/admin/templates/definitions/{id}", func(ctx context.Context, in *templateDefinitionUpdateInput) (*templateDefinitionDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template definition id")
		if err != nil {
			return nil, err
		}
		repoID, err := parseUUID(in.Body.SharedRepoID, "shared repo id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.UpdateTemplate(ctx, id, shared.TemplateDefinitionInput{
			Name: in.Body.Name, SharedRepoID: repoID, PlaybookPath: in.Body.PlaybookPath,
			Limit: in.Body.Limit, ExtraVars: in.Body.ExtraVars, ExtraVarsSchema: in.Body.ExtraVarsSchema,
		})
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "updated shared job template definition "+d.Name)
		return templateDefinitionDetail(d), nil
	}, withAudit("updateTemplateDefinition", "shared_job_template"))

	huma.Delete(hapi, "/api/v1/admin/templates/definitions/{id}", func(ctx context.Context, in *templateDefinitionGetInput) (*okOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template definition id")
		if err != nil {
			return nil, err
		}
		if err := deps.Shared.DeleteTemplate(ctx, id); err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "deleted shared job template definition")
		return &okOutput{}, nil
	}, withAudit("deleteTemplateDefinition", "shared_job_template"))
}

// --- check definitions (Task 7) ---
//
// The same shape as the repository and job template definition DTOs and
// routes above, the third kind reusing the same instance-admin mechanism
// (requireInstanceAdmin, mapSharedErr) rather than a third, parallel set of
// helpers.

// checkDefinitionDefDTO is the installation-owned half of a shared check
// definition (spec v0.2f decision 2): key, plugin, params and interval —
// every organization's own checkDefinitionDTO (check_handlers.go) echoes
// these read-only once shared_definition_id is set. No binding, no enabled
// flag — those are the projection's own, per decision 2's table for check
// definitions specifically (the OPPOSITE split from a repository or job
// template, where the binding is what the projection keeps). Named
// checkDefinitionDefDTO, not checkDefinitionDTO, because that name is
// already taken by check_handlers.go's own organization-scoped DTO for a
// very different shape (it carries a binding; this one never does).
type checkDefinitionDefDTO struct {
	ID              string         `json:"id"`
	Key             string         `json:"key"`
	Plugin          string         `json:"plugin" enum:"cpu,memory,disk,service"`
	Params          map[string]any `json:"params" doc:"Plugin parameters; {} for a plugin that takes none"`
	IntervalSeconds int            `json:"interval_seconds"`
}

func toCheckDefinitionDefDTO(d shared.CheckDefinition) checkDefinitionDefDTO {
	params := d.Params
	if params == nil {
		params = map[string]any{}
	}
	return checkDefinitionDefDTO{
		ID: d.ID.String(), Key: d.Key, Plugin: d.Plugin, Params: params,
		IntervalSeconds: int(d.Interval / time.Second),
	}
}

type checkDefinitionDefListOutput struct {
	Body struct {
		Definitions []checkDefinitionDefDTO `json:"definitions"`
	}
}

type checkDefinitionDefGetInput struct {
	ID string `path:"id"`
}

type checkDefinitionDefDetailOutput struct {
	Body struct {
		Definition checkDefinitionDefDTO `json:"definition"`
	}
}

// checkDefinitionDefBody is the editable shape both create and update take —
// repoDefinitionBody's own doc comment, same convention: shared.
// CheckDefinitionInput itself enforces no format rules of its own at THIS
// layer (plugin, key and params all go through
// checks.ValidateDefinitionShape inside shared.Service instead — the one
// validator every write funnels through, the same "one validator, one
// message" principle check_handlers.go's own checkDefinitionBody states for
// an organization's own definition). Key is optional on create (defaults to
// the plugin name, same as an organization's own) but — like
// check_handlers.go's own body — not defaulted away on an update by this
// schema; shared.Service.UpdateCheckDefinition enforces that the same way
// checks.Service.UpdateDefinition already does.
type checkDefinitionDefBody struct {
	Key             string         `json:"key,omitempty" doc:"Lower-case identifier a narrower scope overrides on inside every organization. Defaults to the plugin name on create."`
	Plugin          string         `json:"plugin" doc:"One of cpu, memory, disk, service"`
	Params          map[string]any `json:"params,omitempty" doc:"disk takes an optional mounts list, service a required units list; cpu and memory take none"`
	IntervalSeconds int            `json:"interval_seconds" doc:"How often the check runs, between 10 and 86400 seconds"`
}

type checkDefinitionDefCreateInput struct {
	Body checkDefinitionDefBody
}

type checkDefinitionDefUpdateInput struct {
	ID   string `path:"id"`
	Body checkDefinitionDefBody
}

func checkDefinitionDefDetail(d shared.CheckDefinition) *checkDefinitionDefDetailOutput {
	out := &checkDefinitionDefDetailOutput{}
	out.Body.Definition = toCheckDefinitionDefDTO(d)
	return out
}

func toCheckDefinitionInput(b checkDefinitionDefBody) shared.CheckDefinitionInput {
	return shared.CheckDefinitionInput{
		Key: b.Key, Plugin: b.Plugin, Params: b.Params, Interval: time.Duration(b.IntervalSeconds) * time.Second,
	}
}

// registerSharedCheckRoutes wires the installation admin's own CRUD over
// check definitions — registerSharedRepoRoutes' own doc comment, same gate,
// same per-verb 403 reasoning (TestCheckDefinitionRoutesRequireInstanceAdmin),
// same root-API registration (no {org} in the path, no bearer token
// admitted).
func registerSharedCheckRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/checks/definitions", func(ctx context.Context, _ *struct{}) (*checkDefinitionDefListOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		list, err := deps.Shared.ListCheckDefs(ctx)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		out := &checkDefinitionDefListOutput{}
		out.Body.Definitions = make([]checkDefinitionDefDTO, len(list))
		for i, d := range list {
			out.Body.Definitions[i] = toCheckDefinitionDefDTO(d)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/admin/checks/definitions", func(ctx context.Context, in *checkDefinitionDefCreateInput) (*checkDefinitionDefDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.CreateCheckDefinition(ctx, toCheckDefinitionInput(in.Body))
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "created shared check definition "+d.Key)
		return checkDefinitionDefDetail(d), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createCheckDefinition", "shared_check_definition"))

	huma.Get(hapi, "/api/v1/admin/checks/definitions/{id}", func(ctx context.Context, in *checkDefinitionDefGetInput) (*checkDefinitionDefDetailOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "check definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.GetCheckDef(ctx, id)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		return checkDefinitionDefDetail(d), nil
	})

	huma.Put(hapi, "/api/v1/admin/checks/definitions/{id}", func(ctx context.Context, in *checkDefinitionDefUpdateInput) (*checkDefinitionDefDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "check definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.UpdateCheckDefinition(ctx, id, toCheckDefinitionInput(in.Body))
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "updated shared check definition "+d.Key)
		return checkDefinitionDefDetail(d), nil
	}, withAudit("updateCheckDefinition", "shared_check_definition"))

	huma.Delete(hapi, "/api/v1/admin/checks/definitions/{id}", func(ctx context.Context, in *checkDefinitionDefGetInput) (*okOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "check definition id")
		if err != nil {
			return nil, err
		}
		if err := deps.Shared.DeleteCheckDefinition(ctx, id); err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "deleted shared check definition")
		return &okOutput{}, nil
	}, withAudit("deleteCheckDefinition", "shared_check_definition"))
}

// mapSharedErr converts shared.Service sentinels into HTTP errors for the
// instance-admin-only repository-definition routes registered above. It is
// deliberately its own function, never merged into mapReposErr
// (repo_handlers.go) — every caller here already passed
// requireInstanceAdmin, which is what makes it safe for this one function to
// compose a sentence naming another organization (spec decision 7's
// collision message, decision 5's in-use message); mapReposErr backs the
// organization-scoped repository routes a member of ANY organization can
// reach, and must never gain a case that can do the same. See this
// package's TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity, which
// exists specifically to catch the two functions ever being merged or
// either message being composed somewhere an organization-scoped caller can
// reach it.
//
// shared.Service deliberately returns organization ids as data — Orgs
// []uuid.UUID on *NameCollisionError, OrgID on *InUseError (see their own
// doc comments, internal/shared/definition.go) — rather than a formatted
// string, precisely so the one layer allowed to show a name decides how.
// resolveOrgLabel/resolveOrgLabels do that resolution here, in the handler,
// never in shared.Service.
func mapSharedErr(ctx context.Context, deps Deps, err error) error {
	var coll *shared.NameCollisionError
	if errors.As(err, &coll) {
		msg := fmt.Sprintf("name %q is already used by %s", coll.Name, strings.Join(resolveOrgLabels(ctx, deps, coll.Orgs), ", "))
		// coll.Scope is set only for check definitions (NameCollisionError's
		// own doc comment) — a review finding: without it, an instance
		// admin reading "already used by acme" would naturally check
		// acme's own organization-WIDE check list, which can be empty even
		// though acme is exactly the colliding organization, when the
		// collision is at a group or host scope acme itself configured.
		if coll.Scope != "" {
			msg += ", at " + coll.Scope
		}
		return huma.Error409Conflict(msg)
	}
	var inUse *shared.InUseError
	if errors.As(err, &inUse) {
		label := resolveOrgLabel(ctx, deps, inUse.OrgID)
		if len(inUse.Reasons) == 0 {
			return huma.Error409Conflict(fmt.Sprintf("organization %s still has a dependant referencing this definition's projection", label))
		}
		return huma.Error409Conflict(fmt.Sprintf(
			"organization %s: %s still reference this definition's projection", label, strings.Join(inUse.Reasons, ", "),
		))
	}
	var ve *schema.ValidationError
	if errors.Is(err, shared.ErrInvalidExtraVars) && errors.As(err, &ve) {
		return schemaProblems(ve, "body.extra_vars")
	}
	switch {
	case errors.Is(err, shared.ErrNotFound):
		return huma.Error404NotFound("not found")
	// Neither bare sentinel below ever carries an organization (that is
	// exactly what the richer *NameCollisionError/*InUseError forms above
	// are for — see their own doc comments on when the store returns the
	// plain sentinel instead), so passing their own Error() string through
	// is safe here the same way it would NOT be for the richer forms.
	case errors.Is(err, shared.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, shared.ErrInUse):
		return huma.Error409Conflict(err.Error())
	// shared.ErrInvalidInput (Task 6 only, so far: a shared job template's
	// extra-vars schema declaring a secret field, or extra vars naming a
	// forbidden connection-security variable — shared.
	// validateTemplateDefinitionInput's own doc comment). Never carries an
	// organization either.
	case errors.Is(err, shared.ErrInvalidPlaybookPath):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, shared.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped shared error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}

// resolveOrgLabel resolves id to the organization's own slug, for an
// instance-admin-facing message only (every caller of mapSharedErr has
// already passed requireInstanceAdmin). Falling back to the raw id on a
// lookup failure, rather than dropping the organization from the message or
// failing the whole request, is deliberate: an instance admin already sees
// every organization, so the id itself is not a leak here the way it would
// be from an organization-scoped route — it is simply a worse label than
// the slug this function exists to produce.
func resolveOrgLabel(ctx context.Context, deps Deps, id uuid.UUID) string {
	org, err := deps.Tenancy.GetOrgByID(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "resolve organization slug for an instance-admin-facing error", "org_id", id, "err", err)
		return id.String()
	}
	return org.Slug
}

// resolveOrgLabels is resolveOrgLabel over a list, preserving order — order
// matters here only for a stable, testable message; CollidingOrgs
// (internal/shared/definition.go) already sorts ids, so this does not need
// to sort them again.
func resolveOrgLabels(ctx context.Context, deps Deps, ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = resolveOrgLabel(ctx, deps, id)
	}
	return out
}
