package shared

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/playbooks"
)

// Sentinel errors mapped to HTTP status by the transport layer (Task 4),
// the same convention every other domain's service.go follows
// (repos.ErrNotFound and its neighbours, for one).
var (
	// ErrNotFound means the definition does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken means a definition's name collides with something.
	// *NameCollisionError (definition.go) is the richer form decision 7's
	// cross-organization check returns; a plain ErrNameTaken means two
	// DEFINITIONS want the same shared_repos.name, which carries no
	// organization to name.
	ErrNameTaken = errors.New("name already in use")
	// ErrInUse means something still references a definition and its
	// DeleteRepo/DeleteTemplate/DeleteCheckDefinition therefore refused to
	// remove it — spec decision 5. Most of the time that something is a
	// PROJECTION'S own dependant inside one organization, and *InUseError
	// (definition.go) is the richer form naming that organization and,
	// where known, what it is — a job trigger, a playbook or a schedule,
	// today — still references; a vault credential binding never appears
	// here, since it is part of the projection rather than a dependant on
	// it, and goes with a delete rather than blocking one. One dependant is
	// NOT a projection's: shared_job_templates.shared_repo_id references a
	// repository definition directly, at the installation level, so a
	// template definition can block a repository definition's delete too —
	// returned as a plain, definition-naming ErrInUse rather than
	// *InUseError, since there is no organization to attach to it.
	ErrInUse = errors.New("definition is in use")
	// ErrInvalidInput marks a definition input this package can judge
	// without a database: a shared template whose extra-vars schema
	// declares a secret field (schemaHasSecretField's own doc comment,
	// definition.go), or extra vars naming a connection-security variable
	// ansiblevars.ForbiddenIn reserves. RepoDefinitionInput never needed
	// this sentinel — url/branch/subpath format validation is pushed to
	// the API schema layer (shared_handlers.go's own repoDefinitionBody
	// comment) because none of the three carries anything this package
	// alone can judge; a template's extra vars are different; nothing but
	// this package ever sees them before they are written straight into
	// every organization's own job_templates row.
	ErrInvalidInput = errors.New("invalid input")
	// ErrInvalidPlaybookPath is the invalid-input class for a template
	// definition's playbook_path that no repo playbook can have
	// (playbooks.CanonicalRepoPath): the API answers it with 422.
	ErrInvalidPlaybookPath = fmt.Errorf("%w: invalid playbook path", ErrInvalidInput)
)

// Store persists repository, job template and check definitions and their
// projections — the three applications of this package's one mechanism
// (spec v0.2f decisions 1, 2, 4, 5, 7). Every method here except ProjectInto
// and its two namesakes (ProjectAllInto's own per-kind siblings) must be
// called on a context with NO organization bound (db.WithOrgScope never
// called for it): none of the three definition tables carries an org_id or
// row-level security (migration 00060, the instance_settings posture since
// migration 00009), and reaching every organization's own projection rows
// in one call — to read every existing name/key for decision 7's check, or
// to write every projection in one transaction — depends on each projected
// table's own org_isolation_bypass policy, which only admits everything
// when no organization is bound. ProjectInto (and ProjectTemplateInto,
// ProjectCheckDefinitionInto) write exactly one organization's own row and
// are the one method per kind Task 3/6/7 call from inside
// tenancy.Service.CreateOrg's own, already org-bound transaction — see
// ProjectInto's own doc comment below. store_sql.go's doc comment says this
// again at the one place a future reader is likeliest to reach for
// db.WithOrgScope out of habit.
type Store interface {
	// CreateWithProjections inserts def and one projection of it into every
	// organization that exists at the moment this call's own transaction
	// reads the organization list — spec decision 4 — atomically: a brand
	// new definition appears everywhere at once or not at all. The
	// organization list is read inside this call, not supplied by the
	// caller, precisely so there is no separate round trip between
	// "decide who to project into" and "write it" for an organization
	// created in between to fall through. ErrNameTaken when another
	// definition already has def.Name; *NameCollisionError when a race
	// against the caller's own pre-flight check lets a newly created
	// organization's own unrelated row collide with def.Name instead.
	CreateWithProjections(ctx context.Context, def RepoDefinition) (RepoDefinition, error)
	// UpdateAndPropagate updates def's own row and every one of its
	// existing projections' shared columns (name, url, branch, subpath),
	// atomically — Review Focus 2: a failure partway must leave every
	// projection, including ones already written this call, on the
	// previous value. ErrNotFound when def.ID does not exist.
	UpdateAndPropagate(ctx context.Context, def RepoDefinition) (RepoDefinition, error)
	// DeleteWithProjections removes every projection of id and then id
	// itself, atomically — spec decision 5: a definition whose projections
	// are referenced by nothing deletes, taking them with it. ErrInUse,
	// naming the organization, when a projection is still referenced by
	// something inside it; ErrNotFound when id does not exist.
	DeleteWithProjections(ctx context.Context, id uuid.UUID) error
	// ListDefinitions returns every repository definition, oldest first —
	// ProjectAllInto's raw material for a newly created organization.
	ListDefinitions(ctx context.Context) ([]RepoDefinition, error)
	// GetDefinition reads one definition. ErrNotFound when it does not
	// exist.
	GetDefinition(ctx context.Context, id uuid.UUID) (RepoDefinition, error)
	// ProjectInto creates one projection of def in orgID — ProjectAllInto's
	// single-organization, single-definition step. Unlike every other
	// method on this Store, it is expected to run on an org-bound ctx: it
	// writes exactly one organization's own row, which git_repos' ordinary
	// org_isolation policy (not the bypass) already admits, and Task 3
	// calls it from inside tenancy.Service.CreateOrg's own org-scoped
	// transaction for the organization being created. ErrNameTaken if
	// orgID already has a row named def.Name (not expected for a
	// brand-new organization, which cannot yet own any row at all, but
	// handled rather than assumed away).
	ProjectInto(ctx context.Context, def RepoDefinition, orgID uuid.UUID) error
	// ExistingNames returns every (organization, name) pair from the table
	// this definition kind projects into (git_repos), excluding rows that
	// are already a projection of exclude — decision 7's raw material,
	// matched against a candidate name by the pure CollidingOrgs. Pass
	// uuid.Nil for a brand-new definition, which excludes nothing.
	ExistingNames(ctx context.Context, exclude uuid.UUID) ([]OrgName, error)

	// --- job templates (Task 6) ---
	//
	// The same seven methods above, for shared_job_templates/job_templates
	// instead of shared_repos/git_repos — tx.go's own comment on why
	// withTx and propagate are plain functions over *gen.Queries rather
	// than generics is exactly why these are added to THIS interface and
	// implemented by the SAME SQLStore, rather than a second, parallel
	// pair of types.

	// CreateTemplateWithProjections is CreateWithProjections for a job
	// template definition: def's own row and one projection of it into
	// every organization that exists, atomically. ErrNameTaken when
	// another definition already has def.Name; *NameCollisionError on a
	// race against the caller's own pre-flight check; ErrNotFound when
	// def.SharedRepoID does not name an existing repository definition.
	CreateTemplateWithProjections(ctx context.Context, def TemplateDefinition) (TemplateDefinition, error)
	// UpdateTemplateAndPropagate is UpdateAndPropagate for a job template
	// definition: def's own row and every existing projection's shared
	// columns, atomically — including playbook_id, RE-RESOLVED per
	// organization against the (possibly changed) SharedRepoID/PlaybookPath,
	// never merely copied (ProjectedTemplateColumns' own doc comment).
	// ErrNotFound when def.ID, or def.SharedRepoID, does not exist.
	UpdateTemplateAndPropagate(ctx context.Context, def TemplateDefinition) (TemplateDefinition, error)
	// DeleteTemplateWithProjections is DeleteWithProjections for a job
	// template definition: *InUseError, naming the organization and
	// whether a schedule or a job trigger still references the
	// projection (SQLStore.DeleteTemplateWithProjections' own doc comment
	// has the full inventory); ErrNotFound when id does not exist.
	DeleteTemplateWithProjections(ctx context.Context, id uuid.UUID) error
	// ListTemplateDefinitions returns every job template definition,
	// oldest first.
	ListTemplateDefinitions(ctx context.Context) ([]TemplateDefinition, error)
	// GetTemplateDefinition reads one job template definition. ErrNotFound
	// when it does not exist.
	GetTemplateDefinition(ctx context.Context, id uuid.UUID) (TemplateDefinition, error)
	// ProjectTemplateInto creates one projection of def in orgID, resolving
	// playbook_id against orgID's own git_repos/playbooks rows. Expected to
	// run on an org-bound ctx, exactly like ProjectInto. ErrNameTaken if
	// orgID already has a row named def.Name.
	ProjectTemplateInto(ctx context.Context, def TemplateDefinition, orgID uuid.UUID) error
	// ExistingTemplateNames is ExistingNames for job_templates: every
	// (organization, name) pair, excluding rows already a projection of
	// exclude.
	ExistingTemplateNames(ctx context.Context, exclude uuid.UUID) ([]OrgName, error)
	// ResolveTemplatePlaybooksForNewPath is the organization-side half of
	// playbook_id resolution (ResolveTemplatePlaybooks' own doc comment on
	// Service has the full reasoning): fills in playbook_id for every one
	// of orgID's own job_templates projections still waiting on exactly
	// (repoID, path) — orgID's own git_repos row and the path a playbook
	// was just registered at. Touches zero rows harmlessly when nothing is
	// waiting on that pair.
	ResolveTemplatePlaybooksForNewPath(ctx context.Context, orgID, repoID, playbookID uuid.UUID, path string) error

	// --- check definitions (Task 7) ---
	//
	// The same seven-method shape above, for shared_check_definitions/
	// check_definitions instead of shared_repos/git_repos or
	// shared_job_templates/job_templates — tx.go's own comment, unchanged
	// reasoning, is why these are added to THIS interface and implemented
	// by the SAME SQLStore rather than a third, parallel pair of types.
	// There is no ResolveCheckDefinitionPlaybooksForNewPath-equivalent: a
	// check definition's binding needs no per-organization resolution at
	// all (queries/shared.sql's own comment block on check definitions).

	// CreateCheckDefinitionWithProjections is CreateWithProjections for a
	// check definition: def's own row and one projection of it — binding
	// ('org', nil, nil), enabled true, origin "shared" — into every
	// organization that exists, atomically. ErrNameTaken when another
	// definition already has def.Key; *NameCollisionError on a race against
	// the caller's own pre-flight check.
	CreateCheckDefinitionWithProjections(ctx context.Context, def CheckDefinition) (CheckDefinition, error)
	// UpdateCheckDefinitionAndPropagate is UpdateAndPropagate for a check
	// definition: def's own row and every existing projection's shared
	// columns (key, plugin, params, interval_seconds), atomically — NEVER
	// binding_kind, group_id, host_id or enabled, which stay exactly as
	// each organization last set them (UpdateProjectedCheckDefinition's own
	// doc comment). ErrNotFound when def.ID does not exist.
	UpdateCheckDefinitionAndPropagate(ctx context.Context, def CheckDefinition) (CheckDefinition, error)
	// DeleteCheckDefinitionWithProjections is DeleteWithProjections for a
	// check definition. UNLIKE repositories and job templates there is no
	// dependant table to check first — nothing in this schema references
	// check_definitions by foreign key (migration 00062's own comment) — so
	// this never returns *InUseError; ErrNotFound when id does not exist.
	DeleteCheckDefinitionWithProjections(ctx context.Context, id uuid.UUID) error
	// ListCheckDefinitions returns every check definition, oldest first.
	ListCheckDefinitions(ctx context.Context) ([]CheckDefinition, error)
	// GetCheckDefinition reads one check definition. ErrNotFound when it
	// does not exist.
	GetCheckDefinition(ctx context.Context, id uuid.UUID) (CheckDefinition, error)
	// ProjectCheckDefinitionInto creates one projection of def in orgID —
	// ProjectInto's own single-organization, single-definition shape.
	// Expected to run on an org-bound ctx, exactly like ProjectInto.
	// ErrNameTaken if orgID already has a definition named def.Key at the
	// default ('org', nil, nil) scope (not expected for a brand-new
	// organization, which cannot yet own any row at all, but handled rather
	// than assumed away).
	ProjectCheckDefinitionInto(ctx context.Context, def CheckDefinition, orgID uuid.UUID) error
	// ExistingCheckDefinitionNames returns every (organization, key) pair at
	// the default ('org', nil, nil) scope — decision 7's raw material for
	// check definitions, matched against a candidate key by the pure
	// CollidingOrgs, excluding rows already a projection of exclude.
	// Documented as an intentionally incomplete preflight (not every
	// organization's projection still sits at this scope) by
	// queries/shared.sql's own comment on the query this delegates to; the
	// per-row propagate loop's own unique-violation catch is the
	// authoritative backstop for a projection that has moved elsewhere.
	ExistingCheckDefinitionNames(ctx context.Context, exclude uuid.UUID) ([]OrgName, error)
}

// Service implements the installation-admin operations on all three kinds
// of installation-owned definition — repositories, job templates and check
// definitions: define one, change one, remove one, and seed one into a
// newly created organization. Every method takes Principal.InstanceAdmin
// as a decision already made by its caller (spec decision 8) — this
// package has no Principal and no HTTP request to check one against, and
// enforcing that decision is Task 4's handlers' job, not this service's.
// A caller that reaches this Service without having made that check first
// is a bug in that caller, not something this type can detect or guard
// against from here.
type Service struct {
	store Store
}

// NewService wires the store.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// CreateRepo defines a new shared repository and projects it into every
// existing organization at once (spec decision 4). A name already used by
// another definition is ErrNameTaken; a name already used by some
// organization's own, unrelated repository is a *NameCollisionError naming
// every organization that has it (spec decision 7) — checked before the
// definition is written at all, so a refused create never partially
// exists.
func (s *Service) CreateRepo(ctx context.Context, in RepoDefinitionInput) (RepoDefinition, error) {
	existing, err := s.store.ExistingNames(ctx, uuid.Nil)
	if err != nil {
		return RepoDefinition{}, fmt.Errorf("list existing repo names: %w", err)
	}
	if colliding := CollidingOrgs(existing, in.Name); len(colliding) > 0 {
		return RepoDefinition{}, &NameCollisionError{Name: in.Name, Orgs: colliding}
	}
	def := RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: in.Name, URL: in.URL,
		Branch: defaultBranch(in.Branch), Subpath: in.Subpath,
	}
	return s.store.CreateWithProjections(ctx, def)
}

// UpdateRepo replaces def's editable fields and propagates the shared ones
// to every existing projection (spec decision 2), atomically (Review
// Focus 2). The same two collision cases as CreateRepo apply to a rename;
// the collision check excludes id's own current projections (they are what
// is about to be renamed, not something it could collide with).
func (s *Service) UpdateRepo(ctx context.Context, id uuid.UUID, in RepoDefinitionInput) (RepoDefinition, error) {
	existing, err := s.store.ExistingNames(ctx, id)
	if err != nil {
		return RepoDefinition{}, fmt.Errorf("list existing repo names: %w", err)
	}
	if colliding := CollidingOrgs(existing, in.Name); len(colliding) > 0 {
		return RepoDefinition{}, &NameCollisionError{Name: in.Name, Orgs: colliding}
	}
	def := RepoDefinition{ID: id, Name: in.Name, URL: in.URL, Branch: defaultBranch(in.Branch), Subpath: in.Subpath}
	return s.store.UpdateAndPropagate(ctx, def)
}

// DeleteRepo removes a definition and every one of its projections
// (ErrInUse, naming the organization, while any projection is still
// referenced by something inside it — spec decision 5).
func (s *Service) DeleteRepo(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteWithProjections(ctx, id)
}

// ProjectAllInto gives orgID a projection of every repository definition
// that already exists — spec decision 4, the seam tenancy.Service.CreateOrg
// calls (Task 3) beside the one rules.SeedForOrg already uses, for a newly
// created organization rather than an existing one receiving a new
// definition (CreateRepo's own job). A failure partway is reported rather
// than retried or rolled back here — the same posture SeedForOrg's own
// comment documents for its five rules, and for the same reason: this call
// has no transaction of its own to roll back into, and whether it runs
// inside the caller's (Task 3 wires CreateOrg's own transaction around it)
// is that caller's decision, not this one's.
func (s *Service) ProjectAllInto(ctx context.Context, orgID uuid.UUID) error {
	defs, err := s.store.ListDefinitions(ctx)
	if err != nil {
		return fmt.Errorf("list repo definitions: %w", err)
	}
	for _, def := range defs {
		if err := s.store.ProjectInto(ctx, def, orgID); err != nil {
			return fmt.Errorf("project repo definition %q into organization: %w", def.Name, err)
		}
	}
	return nil
}

// GetRepo reads one definition. ErrNotFound when it does not exist.
func (s *Service) GetRepo(ctx context.Context, id uuid.UUID) (RepoDefinition, error) {
	return s.store.GetDefinition(ctx, id)
}

// ListRepos returns every repository definition, oldest first.
func (s *Service) ListRepos(ctx context.Context) ([]RepoDefinition, error) {
	return s.store.ListDefinitions(ctx)
}

// --- job templates (Task 6) ---

// CreateTemplate defines a new shared job template and projects it into
// every existing organization at once (spec decision 4). A name already
// used by another definition is ErrNameTaken; a name already used by some
// organization's own, unrelated template is a *NameCollisionError naming
// every organization that has it (spec decision 7); a schema declaring a
// secret field, or extra vars naming a forbidden connection-security
// variable, is ErrInvalidInput (schemaHasSecretField's own doc comment,
// definition.go) — all checked before the definition is written at all.
func (s *Service) CreateTemplate(ctx context.Context, in TemplateDefinitionInput) (TemplateDefinition, error) {
	if err := validateTemplateDefinitionInput(in); err != nil {
		return TemplateDefinition{}, err
	}
	existing, err := s.store.ExistingTemplateNames(ctx, uuid.Nil)
	if err != nil {
		return TemplateDefinition{}, fmt.Errorf("list existing template names: %w", err)
	}
	if colliding := CollidingOrgs(existing, in.Name); len(colliding) > 0 {
		return TemplateDefinition{}, &NameCollisionError{Name: in.Name, Orgs: colliding}
	}
	def := TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: in.Name, SharedRepoID: in.SharedRepoID,
		PlaybookPath: in.PlaybookPath, Limit: in.Limit, ExtraVars: extraVarsOrEmpty(in.ExtraVars), ExtraVarsSchema: in.ExtraVarsSchema,
	}
	return s.store.CreateTemplateWithProjections(ctx, def)
}

// UpdateTemplate replaces def's editable fields and propagates the shared
// ones to every existing projection, atomically, re-resolving playbook_id
// per organization. The same checks as CreateTemplate apply to a rename;
// the collision check excludes id's own current projections.
func (s *Service) UpdateTemplate(ctx context.Context, id uuid.UUID, in TemplateDefinitionInput) (TemplateDefinition, error) {
	if err := validateTemplateDefinitionInput(in); err != nil {
		return TemplateDefinition{}, err
	}
	existing, err := s.store.ExistingTemplateNames(ctx, id)
	if err != nil {
		return TemplateDefinition{}, fmt.Errorf("list existing template names: %w", err)
	}
	if colliding := CollidingOrgs(existing, in.Name); len(colliding) > 0 {
		return TemplateDefinition{}, &NameCollisionError{Name: in.Name, Orgs: colliding}
	}
	def := TemplateDefinition{
		ID: id, Name: in.Name, SharedRepoID: in.SharedRepoID,
		PlaybookPath: in.PlaybookPath, Limit: in.Limit, ExtraVars: extraVarsOrEmpty(in.ExtraVars), ExtraVarsSchema: in.ExtraVarsSchema,
	}
	return s.store.UpdateTemplateAndPropagate(ctx, def)
}

// DeleteTemplate removes a job template definition and every one of its
// projections (ErrInUse, naming the organization, while any projection is
// still referenced by a schedule or a job trigger — spec decision 5).
func (s *Service) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteTemplateWithProjections(ctx, id)
}

// ProjectAllTemplatesInto gives orgID a projection of every job template
// definition that already exists — ProjectAllInto's own counterpart for
// templates, the same seam for a newly created organization.
func (s *Service) ProjectAllTemplatesInto(ctx context.Context, orgID uuid.UUID) error {
	defs, err := s.store.ListTemplateDefinitions(ctx)
	if err != nil {
		return fmt.Errorf("list template definitions: %w", err)
	}
	for _, def := range defs {
		if err := s.store.ProjectTemplateInto(ctx, def, orgID); err != nil {
			return fmt.Errorf("project template definition %q into organization: %w", def.Name, err)
		}
	}
	return nil
}

// GetTemplateDef reads one job template definition. ErrNotFound when it
// does not exist.
func (s *Service) GetTemplateDef(ctx context.Context, id uuid.UUID) (TemplateDefinition, error) {
	return s.store.GetTemplateDefinition(ctx, id)
}

// ListTemplateDefs returns every job template definition, oldest first.
func (s *Service) ListTemplateDefs(ctx context.Context) ([]TemplateDefinition, error) {
	return s.store.ListTemplateDefinitions(ctx)
}

// ResolveTemplatePlaybooks is playbooks.TemplateResolver's own method —
// internal/playbooks cannot import this package (templates/repos both sit
// below shared in the dependency graph the layering rule draws, and a
// cross-import back up would cycle), so playbooks.Service depends on this
// signature as its own, narrow interface instead, exactly the shape
// playbooks.RepoSource already uses for internal/repos.
//
// Called from playbooks.Service.CreateRepo right after a new (org, repo,
// path) playbook is stored: the ORGANIZATION-side event that makes a
// shared template's projection resolvable, which nothing reacted to before
// this method existed (review finding on Task 6's first draft —
// CreateTemplateWithProjections and UpdateTemplateAndPropagate only ever
// resolved playbook_id on a DEFINITION-side event, leaving a template
// defined before an organization had synced its matching playbook
// permanently unresolved until the definition's next edit). A provider
// configuring centrally before any customer has registered the matching
// path is the normal order this feature exists for, not a corner case.
func (s *Service) ResolveTemplatePlaybooks(ctx context.Context, orgID, repoID, playbookID uuid.UUID, path string) error {
	return s.store.ResolveTemplatePlaybooksForNewPath(ctx, orgID, repoID, playbookID, path)
}

// validateTemplateDefinitionInput runs the checks that belong to THIS
// package rather than the API schema layer (ErrInvalidInput's own doc
// comment explains why a template's extra vars are different from a
// repository definition's plain string fields): a well-formed schema
// (schema.Check) with no secret-typed field and no field taken from a
// playbook, and none of the connection-security variables
// internal/ansiblevars reserves — the same denylist
// internal/templates.Service.build applies to an ordinary template's extra
// vars, reused here because a shared definition's extra vars reach every
// organization's own job_templates row WITHOUT ever passing through that
// service (store_sql.go writes them directly, exactly like a repository
// projection's name/url/branch/subpath). The playbook_path must be a
// canonical repo playbook path (playbooks.CanonicalRepoPath): it resolves
// against each organization's playbooks rows by exact path, so any other
// spelling would never resolve.
func validateTemplateDefinitionInput(in TemplateDefinitionInput) error {
	if !playbooks.CanonicalRepoPath(in.PlaybookPath) {
		return fmt.Errorf("%w: playbook_path must be %s", ErrInvalidPlaybookPath, playbooks.RepoPathRule)
	}
	if in.ExtraVarsSchema != nil && schemaHasSecretField(in.ExtraVarsSchema.Fields) {
		return fmt.Errorf("%w: a shared template's extra vars schema may not declare a secret field — a shared definition carries no secret by construction", ErrInvalidInput)
	}
	// The schema reaches every organization's job_templates row without
	// passing through internal/templates, whose build runs schema.Check,
	// so it is checked here (backlog 7.75). A shared definition holds
	// manual fields only: the playbook source sync applies to an
	// organization's own job templates, never to a definition projected
	// into every organization.
	if in.ExtraVarsSchema != nil {
		if err := in.ExtraVarsSchema.Check(); err != nil {
			return fmt.Errorf("%w: extra vars schema: %w", ErrInvalidInput, err)
		}
		if err := in.ExtraVarsSchema.CheckManualOnly(); err != nil {
			return fmt.Errorf("%w: extra vars schema: %w", ErrInvalidInput, err)
		}
	}
	vars := extraVarsOrEmpty(in.ExtraVars)
	if names := ansiblevars.ForbiddenIn(vars); len(names) > 0 {
		return fmt.Errorf("%w: extra vars name a reserved variable: %s", ErrInvalidInput, strings.Join(names, ", "))
	}
	// ForbiddenIn only looks at TOP-LEVEL names, and a nested object
	// with an "__ansible" key that is not a well-formed marker is what
	// makes Ansible's JSON load of a run's extra-vars file fail. The
	// same check internal/templates.Service.build applies to an
	// ordinary template (backlog 2.71), and it matters MORE here: a
	// shared definition's extra vars are written straight into every
	// organization's own job_templates row, so one malformed marker
	// breaks a run in each of them, with a parse error in a document
	// none of their operators wrote (backlog 2.101).
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if err := ansiblevars.CheckMarkers(vars[name]); err != nil {
			return fmt.Errorf("%w: extra var %s cannot be loaded by Ansible: %w", ErrInvalidInput, name, err)
		}
	}
	return nil
}

// extraVarsOrEmpty normalizes a nil extra-vars map to an empty one, the
// same normalization internal/templates.Service.build applies, so a
// definition created with no extra vars at all round-trips as {} rather
// than null.
func extraVarsOrEmpty(vars map[string]any) map[string]any {
	if vars == nil {
		return map[string]any{}
	}
	return vars
}

// defaultBranch mirrors repos.Service's own handling of an empty branch
// field (repos' defaultBranch, service.go): a checkout still needs one to
// name, and DefaultBranch is today's overwhelmingly common one.
func defaultBranch(b string) string {
	if b == "" {
		return DefaultBranch
	}
	return b
}

// --- check definitions (Task 7) ---

// CreateCheckDefinition defines a new shared check definition and projects
// it into every existing organization at once (spec decision 4), at the
// default ('org', nil, nil) binding, enabled — CreateRepo's own doc
// comment, same ordering (validate shape, pre-flight collision check, then
// write) and the same two collision cases. validateCheckDefinitionInput
// applies the same plugin/key/params/interval rules
// checks.ValidateDefinitionShape already enforces for an organization's own
// definition — "Reuse, do not copy".
func (s *Service) CreateCheckDefinition(ctx context.Context, in CheckDefinitionInput) (CheckDefinition, error) {
	key, err := validateCheckDefinitionInput(in)
	if err != nil {
		return CheckDefinition{}, err
	}
	existing, err := s.store.ExistingCheckDefinitionNames(ctx, uuid.Nil)
	if err != nil {
		return CheckDefinition{}, fmt.Errorf("list existing check definition keys: %w", err)
	}
	if colliding := CollidingOrgs(existing, key); len(colliding) > 0 {
		return CheckDefinition{}, &NameCollisionError{Name: key, Orgs: colliding, Scope: checkDefinitionOrgScopeDescription}
	}
	def := CheckDefinition{
		ID: uuid.Must(uuid.NewV7()), Key: key, Plugin: strings.TrimSpace(in.Plugin),
		Params: checkParamsOrEmpty(in.Params), Interval: in.Interval,
	}
	return s.store.CreateCheckDefinitionWithProjections(ctx, def)
}

// UpdateCheckDefinition replaces def's editable fields and propagates the
// shared ones to every existing projection (spec decision 2), atomically.
// The same checks as CreateCheckDefinition apply to a rename; the collision
// check excludes id's own current projections.
func (s *Service) UpdateCheckDefinition(ctx context.Context, id uuid.UUID, in CheckDefinitionInput) (CheckDefinition, error) {
	key, err := validateCheckDefinitionInput(in)
	if err != nil {
		return CheckDefinition{}, err
	}
	existing, err := s.store.ExistingCheckDefinitionNames(ctx, id)
	if err != nil {
		return CheckDefinition{}, fmt.Errorf("list existing check definition keys: %w", err)
	}
	if colliding := CollidingOrgs(existing, key); len(colliding) > 0 {
		return CheckDefinition{}, &NameCollisionError{Name: key, Orgs: colliding, Scope: checkDefinitionOrgScopeDescription}
	}
	def := CheckDefinition{
		ID: id, Key: key, Plugin: strings.TrimSpace(in.Plugin),
		Params: checkParamsOrEmpty(in.Params), Interval: in.Interval,
	}
	return s.store.UpdateCheckDefinitionAndPropagate(ctx, def)
}

// DeleteCheckDefinition removes a check definition and every one of its
// projections. UNLIKE DeleteRepo/DeleteTemplate this never returns
// *InUseError: nothing in this schema references check_definitions at all
// (migration 00062's own comment, and this package's own
// TestDeleteCheckDefinitionWithProjectionsHasNoDependantToCheck).
func (s *Service) DeleteCheckDefinition(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteCheckDefinitionWithProjections(ctx, id)
}

// ProjectAllCheckDefinitionsInto gives orgID a projection of every check
// definition that already exists — ProjectAllInto's own counterpart for
// check definitions, the same seam for a newly created organization.
func (s *Service) ProjectAllCheckDefinitionsInto(ctx context.Context, orgID uuid.UUID) error {
	defs, err := s.store.ListCheckDefinitions(ctx)
	if err != nil {
		return fmt.Errorf("list check definitions: %w", err)
	}
	for _, def := range defs {
		if err := s.store.ProjectCheckDefinitionInto(ctx, def, orgID); err != nil {
			return fmt.Errorf("project check definition %q into organization: %w", def.Key, err)
		}
	}
	return nil
}

// GetCheckDef reads one check definition. ErrNotFound when it does not
// exist.
func (s *Service) GetCheckDef(ctx context.Context, id uuid.UUID) (CheckDefinition, error) {
	return s.store.GetCheckDefinition(ctx, id)
}

// ListCheckDefs returns every check definition, oldest first.
func (s *Service) ListCheckDefs(ctx context.Context) ([]CheckDefinition, error) {
	return s.store.ListCheckDefinitions(ctx)
}

// reservedSeedKeys are the exact keys internal/checks.SeedDefinitions()
// stamps into EVERY organization at the org-wide scope — present the
// instant an organization is created and never absent afterward. A shared
// check definition's own pre-flight collision check
// (ExistingCheckDefinitionNames, called right after this function in
// CreateCheckDefinition/UpdateCheckDefinition) can only catch a definition
// reusing one of these when at least one organization already exists to
// compare against: on a genuinely fresh install, or an installation whose
// last organization has since been deleted, there is nothing to collide
// with, and the collision check alone would let the definition through.
// The consequence is not merely "a confusing duplicate key" — it is a
// HALF-PROVISIONED organization: tenancy.Service.CreateOrg commits the
// organization row and its owner's membership BEFORE running the
// organization-created hook chain (tenancy/service.go's own doc comment
// explains why: no transaction spans both), so the very first
// ProjectAllCheckDefinitionsInto call for the very next organization
// created would fail on the real collision this reserved key finally has
// something to hit, leaving CreateOrg's own caller an error and an
// organization that nonetheless exists, with its owner already a member.
// Refusing these four keys unconditionally, independent of whether any
// collision can currently be demonstrated, is what closes that window
// rather than narrowing it.
var reservedSeedKeys = map[string]bool{
	checks.PluginCPU: true, checks.PluginMemory: true, checks.PluginDisk: true, checks.PluginService: true,
}

// checkDefinitionOrgScopeDescription names the scope both
// CreateCheckDefinition/UpdateCheckDefinition's own pre-flight check
// (CollidingOrgs over ExistingCheckDefinitionNames) and
// collisionFromCheckDefinitionRace (store_sql.go) collide at: the default
// ('org', nil, nil) binding a fresh projection always starts at — the only
// scope either of those two ever examines (NameCollisionError's own doc
// comment explains why that needs saying explicitly for this kind).
const checkDefinitionOrgScopeDescription = "the organization-wide scope every fresh projection starts at"

// checkDefinitionUnknownScopeDescription is UpdateCheckDefinitionAndPropagate's
// own per-organization backstop (store_sql.go): a unique violation caught
// while propagating a rename to an organization that has REBOUND its own
// projection away from the default scope (ListCheckDefinitionKeysAtOrgScopeExcludingDefinition's
// own doc comment on the gap this closes). Which scope exactly is not
// re-queried here — doing so would cost another round trip for a message
// only an instance admin ever sees — so this says plainly that it is NOT
// necessarily the organization-wide one, which is the one fact that
// prevents the review's own finding (checking that organization's org-wide
// list and finding nothing).
const checkDefinitionUnknownScopeDescription = "a scope that organization has configured for its own projection, not necessarily the organization-wide one"

// validateCheckDefinitionInput runs checks.ValidateDefinitionShape — the
// plugin set, the key pattern (defaulting an empty key to the plugin name,
// same as an organization's own create) and each plugin's own parameter
// schema — and re-wraps any failure under THIS package's own ErrInvalidInput
// rather than letting checks.ErrInvalidInput escape: mapSharedErr
// (internal/api/shared_handlers.go) only recognizes shared.ErrInvalidInput,
// the same reason validateTemplateDefinitionInput exists for a shared
// template's extra vars. Then refuses reservedSeedKeys outright — see that
// var's own doc comment for why this cannot be left to the ordinary
// collision check alone.
func validateCheckDefinitionInput(in CheckDefinitionInput) (string, error) {
	key, err := checks.ValidateDefinitionShape(in.Plugin, in.Key, in.Params, in.Interval)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if reservedSeedKeys[key] {
		return "", fmt.Errorf(
			"%w: key %q is reserved for the \"Linux base\" set every organization already has; choose a different key",
			ErrInvalidInput, key)
	}
	return key, nil
}

// checkParamsOrEmpty normalizes a nil params map to an empty one —
// extraVarsOrEmpty's own doc comment, same reasoning, so a definition
// created with no params at all round-trips as {} rather than null.
func checkParamsOrEmpty(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
}
