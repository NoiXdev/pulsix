package assistant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool, so the
// statements run inside the request's organization scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Enabled implements Store.
func (s *SQLStore) Enabled(ctx context.Context, orgID uuid.UUID) (bool, error) {
	on, err := s.q.GetOrgAssistantEnabled(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get assistant switch: %w", err)
	}
	return on, nil
}

// SetEnabled implements Store.
func (s *SQLStore) SetEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error {
	if err := s.q.UpsertOrgAssistantEnabled(ctx, gen.UpsertOrgAssistantEnabledParams{OrgID: orgID, Enabled: enabled}); err != nil {
		return fmt.Errorf("store assistant switch: %w", err)
	}
	return nil
}
