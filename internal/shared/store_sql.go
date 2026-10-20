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
// (internal/db/queries/shared.sql). CreateWithProjections, UpdateAndPropagate
// and DeleteWithProjections each run inside withTx (tx.go): more than one
// statement must commit together or not at all, repos.SQLStore.
// ReplaceRepoVaultIdentities's own reason.
//
// Every method here except ProjectInto must be called on a ctx with NO
// organization bound (db.WithOrgScope is never used for it). shared_repos
// carries no org_id and no row-level security at all (migration 00060, the
// instance_settings posture since 00009), and git_repos' own
// org_isolation_bypass policy (migration 00018) is what then lets a single,
// unscoped transaction see — and write — every organization's projection at
// once. Binding ctx to one organization before reaching here would not fail
// loudly: it would just narrow "every organization" down to one, silently,
// which is worse. ProjectInto is the one exception: it writes exactly one
// organization's own row, which git_repos' ordinary org_isolation policy
// already admits, and Task 3 calls it from inside tenancy.Service.CreateOrg's
// own org-bound transaction — see its own doc comment. Access control
// (Principal.InstanceAdmin) is enforced above this package, by Task 4's
// handlers; Service's own doc comment says the same thing again from the
// rules side.
type SQLStore struct{ d *db.DB }

// NewSQLStore builds the store on d.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d} }

// CreateWithProjections inserts def's own row and one projection of it into
// every organization that exists, inside a single transaction (withTx): a
// brand new definition appears everywhere at once or not at all (spec
// decision 4). The organization list is read from inside this same
// transaction — not supplied by the caller — so there is no separate round
// trip between "decide who to project into" and "write it" for an
// organization created in between to fall through unprojected; Service.
// CreateRepo's own pre-flight collision check still runs first and
// separately (it needs the result before deciding whether to call this at
// all), so a name collision from an organization created in that narrower
// window is still possible in principle, which is why InsertProjectedRepos'
// own unique-violation branch below re-derives a proper *NameCollisionError
// rather than returning a bare, undiagnosable ErrNameTaken.
func (s *SQLStore) CreateWithProjections(ctx context.Context, def RepoDefinition) (RepoDefinition, error) {
	var created RepoDefinition
	err := withTx(ctx, s.d, func(ctx context.Context, q *gen.Queries) error {
		name, url, branch, subpath := ProjectedRepoColumns(def)
		row, err := q.CreateSharedRepo(ctx, gen.CreateSharedRepoParams{
			ID: def.ID, Name: name, Url: url, Branch: branch, Subpath: subpath,
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return ErrNameTaken
			}
			return fmt.Errorf("create shared repo: %w", err)
		}

		orgIDs, err := q.ListOrganizationIDs(ctx)
		if err != nil {
			return fmt.Errorf("list organizations: %w", err)
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
					return collisionFromRace(ctx, q, def.ID, name)
				}
				return fmt.Errorf("insert projected repos: %w", err)
			}
		}

		created = toDefinition(row)
		return nil
	})
	if err != nil {
		return RepoDefinition{}, err
	}
	return created, nil
}

// collisionFromRace rebuilds a *NameCollisionError from inside the same
// transaction that just hit a unique violation projecting into every
// organization — the race CreateWithProjections' own doc comment describes:
// an organization created after Service.CreateRepo's pre-flight
// ExistingNames check ran, already carrying name on a row of its own, which
// that earlier, separate read could not have seen. Re-reading here (with
// excludeDef passed straight through, even though a brand new definition
// has no projections yet to exclude) is what turns an otherwise
// undiagnosable bare ErrNameTaken back into the same actionable,
// organization-naming error the ordinary pre-flight path returns.
func collisionFromRace(ctx context.Context, q *gen.Queries, excludeDef uuid.UUID, name string) error {
	rows, err := q.ListGitRepoNamesExcludingDefinition(ctx, excludeDef)
	if err != nil {
		return fmt.Errorf("re-check colliding organizations: %w", err)
	}
	existing := make([]OrgName, len(rows))
	for i, r := range rows {
		existing[i] = OrgName{OrgID: r.OrgID, Name: r.Name}
	}
	if colliding := CollidingOrgs(existing, name); len(colliding) > 0 {
		return &NameCollisionError{Name: name, Orgs: colliding}
	}
	// Could not reproduce the collision from a fresh read (the conflicting
	// row was itself rolled back by whatever transaction created it, say) —
	// still a real unique-violation this call must report, just without a
	// name to attach.
	return ErrNameTaken
}

// UpdateAndPropagate updates def's own row and loops UpdateProjectedRepo
// over every organization currently holding a projection of it
// (ListProjectedRepos), all inside one transaction (withTx). That
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
// ListProjectedRepos at all — its projection is already gone with it,
// cascaded away through git_repos.org_id's own foreign key (migration
// 00018) — so Review Focus 1 costs this method nothing extra: there is no
// dead organization to skip, because the query never returns it.
func (s *SQLStore) UpdateAndPropagate(ctx context.Context, def RepoDefinition) (RepoDefinition, error) {
	var updated RepoDefinition
	err := withTx(ctx, s.d, func(ctx context.Context, q *gen.Queries) error {
		name, url, branch, subpath := ProjectedRepoColumns(def)
		row, err := q.UpdateSharedRepo(ctx, gen.UpdateSharedRepoParams{
			ID: def.ID, Name: name, Url: url, Branch: branch, Subpath: subpath,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if db.IsUniqueViolation(err) {
				return ErrNameTaken
			}
			return fmt.Errorf("update shared repo: %w", err)
		}

		projections, err := q.ListProjectedRepos(ctx, def.ID)
		if err != nil {
			return fmt.Errorf("list projected repos: %w", err)
		}
		if err := propagate(projections, func(p gen.ListProjectedReposRow) error {
			orgID := p.OrgID
			n, err := q.UpdateProjectedRepo(ctx, gen.UpdateProjectedRepoParams{
				OrgID: orgID, SharedRepoID: def.ID, Name: name, Url: url, Branch: branch, Subpath: subpath,
			})
			if err != nil {
				if db.IsUniqueViolation(err) {
					return &NameCollisionError{Name: name, Orgs: []uuid.UUID{orgID}}
				}
				return fmt.Errorf("propagate to organization %s: %w", orgID, err)
			}
			if n == 0 {
				// This organization's projection vanished between the
				// ListProjectedRepos read above and this statement — only
				// possible if something deleted it out from under an
				// in-flight transaction, a narrower race than Review Focus
				// 1's (which never reaches this loop at all, see the
				// method's own doc comment above). Not a reason to abort
				// propagation to every OTHER organization, which this
				// transaction is still free to commit.
				return nil
			}
			return nil
		}); err != nil {
			return err
		}

		updated = toDefinition(row)
		return nil
	})
	if err != nil {
		return RepoDefinition{}, err
	}
	return updated, nil
}

// DeleteWithProjections checks every organization holding a projection of
// id for a job trigger, refuses the whole call with *InUseError on the
// first one found, and otherwise clears each organization's discovered
// playbooks and deletes its projection before removing id's own row — all
// inside one transaction (withTx), so a refusal discovered partway rolls
// back any projection already deleted this call rather than leaving some
// organizations' projections gone and others not.
//
// The full inventory of every table with a foreign key to git_repos, and
// what this method does about each one (verified against the migrations,
// not assumed from whatever error a DELETE happened to raise — that
// assumption is what let a customer's job trigger disappear silently
// before this review):
//
//   - job_triggers (migration 00026): ON DELETE CASCADE. Checked
//     EXPLICITLY, via GitRepoHasJobTrigger, and blocks with *InUseError —
//     a webhook trigger is a customer's own configuration that would
//     silently vanish otherwise (spec decision 5's own example: "a
//     schedule, a trigger or a job").
//   - git_repo_vault_credentials (migration 00059): ON DELETE CASCADE,
//     and left to cascade on purpose. A vault password binding is PART OF
//     the projection, not a dependant on it (the same reasoning v0.2e's
//     own design gave this table its cascade in the first place) — a
//     repository with vault passwords configured must still be deletable,
//     and its passwords go with it.
//   - playbooks (migration 00018): no ON DELETE clause at all — NO ACTION
//     — and NOT counted as a blocking dependant either, but not left
//     alone: DeletePlaybooksForRepo (below, run per organization right
//     before DeleteProjectedRepo) removes them explicitly. A playbook is
//     discovered by sync, not configured by a customer, so treating it as
//     a reason to refuse would make every shared repository permanently
//     undeletable the moment it synced once. If a job template still
//     names one of those playbooks, DeletePlaybooksForRepo's own DELETE
//     fails on job_templates' (org_id, playbook_id) foreign key (also NO
//     ACTION, migration 00018) and is mapped to *InUseError here too — a
//     job template choosing to run this playbook IS customer
//     configuration, one hop further from git_repos than job_triggers but
//     the same idea.
//
// jobs (run history) carries no foreign key to git_repos at all — verified
// by reading every migration that mentions git_repos — so there is
// nothing for this method to check or clean up there.
func (s *SQLStore) DeleteWithProjections(ctx context.Context, id uuid.UUID) error {
	return withTx(ctx, s.d, func(ctx context.Context, q *gen.Queries) error {
		projections, err := q.ListProjectedRepos(ctx, id)
		if err != nil {
			return fmt.Errorf("list projected repos: %w", err)
		}

		for _, p := range projections {
			has, err := q.GitRepoHasJobTrigger(ctx, gen.GitRepoHasJobTriggerParams{OrgID: p.OrgID, RepoID: p.ID})
			if err != nil {
				return fmt.Errorf("check job triggers in organization %s: %w", p.OrgID, err)
			}
			if has {
				return &InUseError{OrgID: p.OrgID, Reasons: []string{"a job trigger"}}
			}
		}

		if err := propagate(projections, func(p gen.ListProjectedReposRow) error {
			if err := q.DeletePlaybooksForRepo(ctx, gen.DeletePlaybooksForRepoParams{OrgID: p.OrgID, RepoID: p.ID}); err != nil {
				if db.IsForeignKeyViolation(err) {
					// A job template still names one of this organization's
					// discovered playbooks — see the method's own doc
					// comment for why that is treated as customer
					// configuration too, even though it is one hop removed
					// from git_repos itself.
					return &InUseError{OrgID: p.OrgID, Reasons: []string{"a job template"}}
				}
				return fmt.Errorf("delete playbooks for organization %s: %w", p.OrgID, err)
			}
			if _, err := q.DeleteProjectedRepo(ctx, gen.DeleteProjectedRepoParams{OrgID: p.OrgID, SharedRepoID: id}); err != nil {
				if db.IsForeignKeyViolation(err) {
					// Backstop only: the explicit checks above already
					// refused the whole call if a job trigger existed, and
					// playbooks are already cleared by the time this runs.
					// Reaching this means either a genuine race (something
					// inserted between those checks and this statement) or
					// a future referencing table this method does not yet
					// know about — either way, a real in-use condition,
					// just one this call cannot name a reason for.
					return &InUseError{OrgID: p.OrgID}
				}
				return fmt.Errorf("delete projected repo in organization %s: %w", p.OrgID, err)
			}
			return nil
		}); err != nil {
			return err
		}

		n, err := q.DeleteSharedRepo(ctx, id)
		if err != nil {
			return fmt.Errorf("delete shared repo: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
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
// for its five rules. Unlike every other method on this store, it is
// expected to run on an org-bound ctx (SQLStore's own doc comment).
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

// toDefinition assembles the package's own RepoDefinition from one
// gen.SharedRepo row.
func toDefinition(r gen.SharedRepo) RepoDefinition {
	return RepoDefinition{ID: r.ID, Name: r.Name, URL: r.Url, Branch: r.Branch, Subpath: r.Subpath}
}
