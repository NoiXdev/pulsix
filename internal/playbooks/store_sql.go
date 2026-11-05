package playbooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope.
type SQLStore struct {
	q *gen.Queries
	d *db.DB
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

func (s *SQLStore) Create(ctx context.Context, p Playbook) (Playbook, error) {
	row, err := s.q.CreatePlaybook(ctx, gen.CreatePlaybookParams{
		ID: p.ID, OrgID: p.OrgID, RepoID: db.NullUUID(p.RepoID),
		Path: p.Path, Inline: p.Inline, Source: p.Source, VarSource: p.InlineSource,
	})
	if err != nil {
		// The playbooks CHECK constraints are this store's last line of
		// defense against a hybrid source row: a violation surfaces as the
		// wrapped database error, not a domain sentinel, because only a
		// caller that bypassed the service can produce it.
		return Playbook{}, fmt.Errorf("create playbook: %w", err)
	}
	return toPlaybook(row), nil
}

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Playbook, error) {
	row, err := s.q.GetPlaybook(ctx, gen.GetPlaybookParams{OrgID: orgID, ID: id})
	if err != nil {
		return Playbook{}, mapErr(err)
	}
	return toPlaybook(row), nil
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Playbook, error) {
	rows, err := s.q.ListPlaybooks(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list playbooks: %w", err)
	}
	out := make([]Playbook, len(rows))
	for i, r := range rows {
		out[i] = toPlaybook(r)
	}
	return out, nil
}

func (s *SQLStore) UpdateInline(ctx context.Context, orgID, id uuid.UUID, inline string, source json.RawMessage) (Playbook, error) {
	row, err := s.q.UpdatePlaybookInline(ctx, gen.UpdatePlaybookInlineParams{OrgID: orgID, ID: id, Inline: &inline, VarSource: source})
	if err != nil {
		return Playbook{}, mapErr(err)
	}
	return toPlaybook(row), nil
}

// FindRepo implements Store: the oldest playbook registered for the
// repository path, ErrNotFound without one.
func (s *SQLStore) FindRepo(ctx context.Context, orgID, repoID uuid.UUID, path string) (Playbook, error) {
	row, err := s.q.FindRepoPlaybook(ctx, gen.FindRepoPlaybookParams{OrgID: orgID, RepoID: db.NullUUID(&repoID), Path: &path})
	if err != nil {
		return Playbook{}, mapErr(err)
	}
	return toPlaybook(row), nil
}

// ListByRepo implements Store: the repository's playbooks, oldest first,
// with ID, OrgID, RepoID, Path and Source set.
func (s *SQLStore) ListByRepo(ctx context.Context, orgID, repoID uuid.UUID) ([]Playbook, error) {
	rows, err := s.q.ListRepoPlaybookPaths(ctx, gen.ListRepoPlaybookPathsParams{OrgID: orgID, RepoID: db.NullUUID(&repoID)})
	if err != nil {
		return nil, fmt.Errorf("list repository playbooks: %w", err)
	}
	out := make([]Playbook, len(rows))
	for i, r := range rows {
		out[i] = Playbook{ID: r.ID, OrgID: orgID, RepoID: &repoID, Path: r.Path, Source: SourceRepo}
	}
	return out, nil
}

// repoPathLockKeyPrefix namespaces WithRepoPathLock's key within
// hashtext's output space (sshconf.chainLockKeyPrefix explains why).
const repoPathLockKeyPrefix = "repo-playbook:"

// WithRepoPathLock implements Store: fn runs in one organization-scoped
// write transaction that holds the advisory lock of (repoID, path), so
// every store call made with the context fn receives is part of it, and
// two callers for one path take turns. No unique index keeps a path to one
// row (see FindRepoPlaybook), so this lock is what does.
func (s *SQLStore) WithRepoPathLock(ctx context.Context, orgID, repoID uuid.UUID, path string, fn func(context.Context) error) error {
	return s.d.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		if _, err := s.d.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, repoPathLockKeyPrefix+repoID.String()+":"+path); err != nil {
			return fmt.Errorf("acquire repository playbook lock: %w", err)
		}
		return fn(ctx)
	})
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeletePlaybook(ctx, gen.DeletePlaybookParams{OrgID: orgID, ID: id})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// The job_templates (org_id, playbook_id) foreign key has no
			// cascade on purpose: a playbook still referenced by a template
			// is in use.
			return ErrInUse
		}
		return fmt.Errorf("delete playbook: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func toPlaybook(r gen.Playbook) Playbook {
	return Playbook{
		ID: r.ID, OrgID: r.OrgID, RepoID: db.UUIDPtr(r.RepoID),
		Path: r.Path, Inline: r.Inline, Source: r.Source, InlineSource: r.VarSource,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
