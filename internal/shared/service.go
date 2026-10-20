package shared

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
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
	// ErrInUse means a projection is still referenced by something inside
	// its organization and DeleteRepo therefore refused to remove the
	// definition — spec decision 5. *InUseError (definition.go) is the
	// richer form naming the organization and, where known, what it is —
	// a job trigger or a playbook, today — still references. A vault
	// credential binding never appears here: it is part of the
	// projection, not a dependant on it, and goes with a delete rather
	// than blocking one.
	ErrInUse = errors.New("definition is in use")
)

// Store persists repository definitions and their projections. Every
// method here except ProjectInto must be called on a context with NO
// organization bound (db.WithOrgScope never called for it): shared_repos
// carries no org_id and no row-level security (migration 00060, the
// instance_settings posture since migration 00009), and reaching every
// organization's git_repos rows in one call — to read every existing name
// for decision 7's check, or to write every projection in one transaction —
// depends on git_repos' own org_isolation_bypass policy, which only admits
// everything when no organization is bound. ProjectInto writes exactly one
// organization's own row and is the one method Task 3 calls from inside
// tenancy.Service.CreateOrg's own, already org-bound transaction — see its
// own doc comment below. store_sql.go's doc comment says this again at the
// one place a future reader is likeliest to reach for db.WithOrgScope out
// of habit.
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
}

// Service implements the installation-admin operations on repository
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

// defaultBranch mirrors repos.Service's own handling of an empty branch
// field (repos' defaultBranch, service.go): a checkout still needs one to
// name, and DefaultBranch is today's overwhelmingly common one.
func defaultBranch(b string) string {
	if b == "" {
		return DefaultBranch
	}
	return b
}
