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
type SQLStore struct {
	q *gen.Queries
	d *db.DB
}

// NewSQLStore builds the store on d itself, never on its raw pool, so the
// statements run inside the request's organization scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

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

// StartsFollowUps implements Store.
func (s *SQLStore) StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error) {
	on, err := s.q.GetOrgAssistantStartsFollowUps(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get follow-up setting: %w", err)
	}
	return on, nil
}

// InTx implements Store: one organization-scoped write transaction that
// every statement run with the context fn receives joins (db.WithWriteTx).
func (s *SQLStore) InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	return s.d.WithWriteTx(ctx, orgID, fn)
}

// Lock implements Store: the row is created with the defaults first, so
// even an organization's first update has a row to lock and a concurrent
// first update waits for it instead of inserting beside it.
func (s *SQLStore) Lock(ctx context.Context, orgID uuid.UUID) (Settings, error) {
	if err := s.q.EnsureOrgAssistantSettings(ctx, orgID); err != nil {
		return Settings{}, fmt.Errorf("create assistant settings: %w", err)
	}
	row, err := s.q.LockOrgAssistantSettings(ctx, orgID)
	if err != nil {
		return Settings{}, fmt.Errorf("lock assistant settings: %w", err)
	}
	return Settings{Enabled: row.Enabled, StartsFollowUps: row.ApprovedRunsStartFollowUps}, nil
}

// Put implements Store.
func (s *SQLStore) Put(ctx context.Context, orgID uuid.UUID, st Settings) error {
	if err := s.q.UpdateOrgAssistantSettings(ctx, gen.UpdateOrgAssistantSettingsParams{
		OrgID: orgID, Enabled: st.Enabled, ApprovedRunsStartFollowUps: st.StartsFollowUps,
	}); err != nil {
		return fmt.Errorf("store assistant settings: %w", err)
	}
	return nil
}
