package shared

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB
// (internal/db/queries/shared.sql). d is kept alongside the generated
// queries (built fresh per call via gen.New) rather than cached once,
// because CreateWithProjections, UpdateAndPropagate and
// DeleteWithProjections each need their own transaction —
// repos.SQLStore.ReplaceRepoVaultIdentities's own shape, for its own
// reason: more than one statement must commit together or not at all.
//
// Every method here must be called on a ctx with NO organization bound
// (db.WithOrgScope is never used for it). shared_repos carries no org_id
// and no row-level security at all (migration 00060, the instance_settings
// posture since 00009), and git_repos' own org_isolation_bypass policy
// (migration 00018) is what then lets a single, unscoped transaction see —
// and write — every organization's projection at once. Binding ctx to one
// organization before reaching here would not fail loudly: it would just
// narrow "every organization" down to one, silently, which is worse. Access
// control (Principal.InstanceAdmin) is enforced above this package, by
// Task 4's handlers; Service's own doc comment says the same thing again
// from the rules side.
type SQLStore struct{ d *db.DB }

// NewSQLStore builds the store on d.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d} }

// CreateWithProjections inserts def's own row and one projection of it into
// every one of orgIDs inside a single transaction (s.d.Begin), so a brand
// new definition appears everywhere at once or not at all (spec decision
// 4) — the same all-or-nothing posture UpdateAndPropagate takes for a
// rename, and for the identical reason: InsertProjectedRepos is one
// statement for every organization (a lockstep unnest, not a loop), but it
// still must not commit if CreateSharedRepo's own row never does.
func (s *SQLStore) CreateWithProjections(ctx context.Context, def RepoDefinition, orgIDs []uuid.UUID) (RepoDefinition, error) {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return RepoDefinition{}, fmt.Errorf("begin create repo definition: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	name, url, branch, subpath := ProjectedRepoColumns(def)
	row, err := q.CreateSharedRepo(ctx, gen.CreateSharedRepoParams{
		ID: def.ID, Name: name, Url: url, Branch: branch, Subpath: subpath,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return RepoDefinition{}, ErrNameTaken
		}
		return RepoDefinition{}, fmt.Errorf("create shared repo: %w", err)
	}

	if len(orgIDs) > 0 {
		ids := make([]uuid.UUID, len(orgIDs))
		for i := range orgIDs {
			ids[i] = uuid.Must(uuid.NewV7())
		}
		if err := q.InsertProjectedRepos(ctx, gen.InsertProjectedReposParams{
			Ids: ids, OrgIds: orgIDs, Name: name, Url: url, Branch: branch, Subpath: subpath,
			SharedRepoID: db.NullUUID(&def.ID),
		}); err != nil {
			if db.IsUniqueViolation(err) {
				return RepoDefinition{}, ErrNameTaken
			}
			return RepoDefinition{}, fmt.Errorf("insert projected repos: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return RepoDefinition{}, fmt.Errorf("commit create repo definition: %w", err)
	}
	return toDefinition(row), nil
}

// UpdateAndPropagate updates def's own row and loops UpdateProjectedRepo
// over every organization currently holding a projection of it
// (ListProjectedRepoOrgIDs), all inside one transaction (s.d.Begin). That
// transaction is the whole point: a per-organization loop has no
// atomicity of its own the way a single bulk UPDATE would, so without it a
// failure partway (one organization's row rejected by a constraint, say)
// would leave every organization already looped over this call on the NEW
// value and the rest on the old one — exactly the "two new and one old"
// state Review Focus 2 exists to make impossible. Wrapped in a transaction,
// that same failure instead rolls back everything this call touched,
// definition included, leaving every projection on the value it had before
// UpdateAndPropagate was ever called.
//
// An organization deleted before this call began never appears in
// ListProjectedRepoOrgIDs at all — its projection is already gone with it,
// cascaded away through git_repos.org_id's own foreign key (migration
// 00018) — so Review Focus 1 costs this method nothing extra: there is no
// dead organization to skip, because the query never returns it.
func (s *SQLStore) UpdateAndPropagate(ctx context.Context, def RepoDefinition) (RepoDefinition, error) {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return RepoDefinition{}, fmt.Errorf("begin update repo definition: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	name, url, branch, subpath := ProjectedRepoColumns(def)
	row, err := q.UpdateSharedRepo(ctx, gen.UpdateSharedRepoParams{
		ID: def.ID, Name: name, Url: url, Branch: branch, Subpath: subpath,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RepoDefinition{}, ErrNotFound
		}
		if db.IsUniqueViolation(err) {
			return RepoDefinition{}, ErrNameTaken
		}
		return RepoDefinition{}, fmt.Errorf("update shared repo: %w", err)
	}

	orgIDs, err := q.ListProjectedRepoOrgIDs(ctx, def.ID)
	if err != nil {
		return RepoDefinition{}, fmt.Errorf("list projected repo organizations: %w", err)
	}
	for _, orgID := range orgIDs {
		n, err := q.UpdateProjectedRepo(ctx, gen.UpdateProjectedRepoParams{
			OrgID: orgID, SharedRepoID: def.ID, Name: name, Url: url, Branch: branch, Subpath: subpath,
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return RepoDefinition{}, fmt.Errorf("%w: organization %s already has a repository named %q", ErrNameTaken, orgID, name)
			}
			return RepoDefinition{}, fmt.Errorf("propagate to organization %s: %w", orgID, err)
		}
		// n == 0 means this organization's projection vanished between the
		// ListProjectedRepoOrgIDs read above and this statement — only
		// possible if something deleted it out from under an in-flight
		// transaction, a narrower race than Review Focus 1's (which never
		// reaches this loop at all, see the method's own doc comment
		// above). Not a reason to abort propagation to every OTHER
		// organization, which this transaction is still free to commit.
		_ = n
	}

	if err := tx.Commit(ctx); err != nil {
		return RepoDefinition{}, fmt.Errorf("commit update repo definition: %w", err)
	}
	return toDefinition(row), nil
}

// DeleteWithProjections loops DeleteProjectedRepo over every organization
// holding a projection of id, then removes id's own row — all inside one
// transaction, so a projection a schedule or a job still names in one
// organization (git_repos has no cascade from those tables, on purpose)
// rolls the whole delete back rather than leaving some organizations'
// projections gone and others not. Looping (ListProjectedRepoOrgIDs' own
// order) rather than a single bulk DELETE is what lets the foreign-key
// violation be reported as a specific organization (spec decision 5's
// "naming where") instead of a bulk failure that names none of them.
func (s *SQLStore) DeleteWithProjections(ctx context.Context, id uuid.UUID) error {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete repo definition: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	orgIDs, err := q.ListProjectedRepoOrgIDs(ctx, id)
	if err != nil {
		return fmt.Errorf("list projected repo organizations: %w", err)
	}
	for _, orgID := range orgIDs {
		if _, err := q.DeleteProjectedRepo(ctx, gen.DeleteProjectedRepoParams{OrgID: orgID, SharedRepoID: id}); err != nil {
			if db.IsForeignKeyViolation(err) {
				return fmt.Errorf("%w: organization %s still has a playbook or schedule using this repository", ErrInUse, orgID)
			}
			return fmt.Errorf("delete projected repo in organization %s: %w", orgID, err)
		}
	}

	n, err := q.DeleteSharedRepo(ctx, id)
	if err != nil {
		return fmt.Errorf("delete shared repo: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete repo definition: %w", err)
	}
	return nil
}

// ListDefinitions returns every repository definition, oldest first.
func (s *SQLStore) ListDefinitions(ctx context.Context) ([]RepoDefinition, error) {
	rows, err := gen.New(s.d).ListSharedRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("list shared repos: %w", err)
	}
	out := make([]RepoDefinition, len(rows))
	for i, r := range rows {
		out[i] = toDefinition(r)
	}
	return out, nil
}

// GetDefinition reads one definition. ErrNotFound when it does not exist.
func (s *SQLStore) GetDefinition(ctx context.Context, id uuid.UUID) (RepoDefinition, error) {
	row, err := gen.New(s.d).GetSharedRepo(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RepoDefinition{}, ErrNotFound
		}
		return RepoDefinition{}, fmt.Errorf("get shared repo: %w", err)
	}
	return toDefinition(row), nil
}

// ProjectInto creates one projection of def in orgID — a single statement,
// not wrapped in its own transaction: ProjectAllInto's caller (Task 3's
// tenancy.Service.CreateOrg) is the one with a transaction to put this in,
// the same division of labour rules.SeedForOrg's own doc comment describes
// for its five rules.
func (s *SQLStore) ProjectInto(ctx context.Context, def RepoDefinition, orgID uuid.UUID) error {
	name, url, branch, subpath := ProjectedRepoColumns(def)
	err := gen.New(s.d).InsertProjectedRepo(ctx, gen.InsertProjectedRepoParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Url: url, Branch: branch, Subpath: subpath,
		SharedRepoID: db.NullUUID(&def.ID),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return ErrNameTaken
		}
		return fmt.Errorf("insert projected repo: %w", err)
	}
	return nil
}

// ExistingNames returns every organization's git_repos name, excluding
// rows that are already a projection of exclude. Pass uuid.Nil to exclude
// nothing — see ListGitRepoNamesExcludingDefinition's own comment
// (queries/shared.sql) for why that is safe.
func (s *SQLStore) ExistingNames(ctx context.Context, exclude uuid.UUID) ([]OrgName, error) {
	rows, err := gen.New(s.d).ListGitRepoNamesExcludingDefinition(ctx, exclude)
	if err != nil {
		return nil, fmt.Errorf("list existing repo names: %w", err)
	}
	out := make([]OrgName, len(rows))
	for i, r := range rows {
		out[i] = OrgName{OrgID: r.OrgID, Name: r.Name}
	}
	return out, nil
}

// OrgIDs returns every organization's id, oldest first.
func (s *SQLStore) OrgIDs(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := gen.New(s.d).ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organization ids: %w", err)
	}
	return ids, nil
}

// toDefinition assembles the package's own RepoDefinition from one
// gen.SharedRepo row.
func toDefinition(r gen.SharedRepo) RepoDefinition {
	return RepoDefinition{ID: r.ID, Name: r.Name, URL: r.Url, Branch: r.Branch, Subpath: r.Subpath}
}
