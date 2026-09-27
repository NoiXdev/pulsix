package checks

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool, so
// every statement runs inside the organization bound to the request
// context (db.WithOrgScope) — the same posture hosts.SQLStore and
// maintenance.SQLStore document, and monitoring_groups' row-level security
// relies on.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func toGroup(r gen.MonitoringGroup) Group {
	return Group{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Selector: r.Selector,
		Priority: int(r.Priority), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (s *SQLStore) Create(ctx context.Context, g Group) (Group, error) {
	row, err := s.q.CreateMonitoringGroup(ctx, gen.CreateMonitoringGroupParams{
		ID: g.ID, OrgID: g.OrgID, Name: g.Name, Selector: g.Selector, Priority: int32(g.Priority), //nolint:gosec // bounded to int32 by normalize
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Group{}, ErrNameTaken
		}
		return Group{}, fmt.Errorf("create monitoring group: %w", err)
	}
	return toGroup(row), nil
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Group, error) {
	rows, err := s.q.ListMonitoringGroups(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list monitoring groups: %w", err)
	}
	out := make([]Group, len(rows))
	for i, r := range rows {
		out[i] = toGroup(r)
	}
	return out, nil
}

func (s *SQLStore) Update(ctx context.Context, g Group) (Group, error) {
	row, err := s.q.UpdateMonitoringGroup(ctx, gen.UpdateMonitoringGroupParams{
		Name: g.Name, Selector: g.Selector, Priority: int32(g.Priority), //nolint:gosec // bounded to int32 by normalize
		OrgID: g.OrgID, ID: g.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Group{}, ErrNotFound
		}
		if db.IsUniqueViolation(err) {
			return Group{}, ErrNameTaken
		}
		return Group{}, fmt.Errorf("update monitoring group: %w", err)
	}
	return toGroup(row), nil
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteMonitoringGroup(ctx, gen.DeleteMonitoringGroupParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete monitoring group: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
