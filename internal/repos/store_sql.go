package repos

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func (s *SQLStore) Create(ctx context.Context, r Repo) (Repo, error) {
	row, err := s.q.CreateRepo(ctx, gen.CreateRepoParams{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Url: r.URL, Branch: r.Branch,
		Subpath: r.Subpath, CredentialID: db.NullUUID(r.CredentialID),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Repo{}, ErrNameTaken
		}
		return Repo{}, fmt.Errorf("create repo: %w", err)
	}
	return toRepo(row), nil
}

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Repo, error) {
	row, err := s.q.GetRepo(ctx, gen.GetRepoParams{OrgID: orgID, ID: id})
	if err != nil {
		return Repo{}, mapErr(err)
	}
	return toRepo(row), nil
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Repo, error) {
	rows, err := s.q.ListRepos(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	out := make([]Repo, len(rows))
	for i, r := range rows {
		out[i] = toRepo(r)
	}
	return out, nil
}

func (s *SQLStore) Update(ctx context.Context, r Repo) (Repo, error) {
	row, err := s.q.UpdateRepo(ctx, gen.UpdateRepoParams{
		OrgID: r.OrgID, ID: r.ID, Name: r.Name, Url: r.URL, Branch: r.Branch,
		Subpath: r.Subpath, CredentialID: db.NullUUID(r.CredentialID),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Repo{}, ErrNameTaken
		}
		return Repo{}, mapErr(err)
	}
	return toRepo(row), nil
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteRepo(ctx, gen.DeleteRepoParams{OrgID: orgID, ID: id})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// The playbooks (org_id, repo_id) foreign key has no cascade on
			// purpose: a repository still referenced by a playbook is in use.
			return ErrInUse
		}
		return fmt.Errorf("delete repo: %w", err)
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

func toRepo(r gen.GitRepo) Repo {
	return Repo{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, URL: r.Url, Branch: r.Branch, Subpath: r.Subpath,
		CredentialID: db.UUIDPtr(r.CredentialID),
		LastSync:     db.TimePtr(r.LastSync), LastSyncError: textOrEmpty(r.LastSyncError),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// textOrEmpty dereferences an optional text column; the sync result store
// maps NULL and "" to the same "no failure" state.
func textOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
