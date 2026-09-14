package playbooks

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

func (s *SQLStore) Create(ctx context.Context, p Playbook) (Playbook, error) {
	row, err := s.q.CreatePlaybook(ctx, gen.CreatePlaybookParams{
		ID: p.ID, OrgID: p.OrgID, RepoID: db.NullUUID(p.RepoID),
		Path: p.Path, Inline: p.Inline, Source: p.Source,
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

func (s *SQLStore) UpdateInline(ctx context.Context, orgID, id uuid.UUID, inline string) (Playbook, error) {
	row, err := s.q.UpdatePlaybookInline(ctx, gen.UpdatePlaybookInlineParams{OrgID: orgID, ID: id, Inline: &inline})
	if err != nil {
		return Playbook{}, mapErr(err)
	}
	return toPlaybook(row), nil
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
		Path: r.Path, Inline: r.Inline, Source: r.Source,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
