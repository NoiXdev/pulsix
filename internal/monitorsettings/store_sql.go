package monitorsettings

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store on org_monitoring_settings. It runs on *db.DB,
// so every statement runs inside the context's organization scope (the
// API binds the request's), the same seam as the other stores.
type SQLStore struct {
	q *gen.Queries
}

// NewSQLStore builds the store on d itself, never on its raw pool.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Get implements Store.
func (s *SQLStore) Get(ctx context.Context, orgID uuid.UUID) (Settings, bool, error) {
	days, err := s.q.GetOrgMonitoringSettings(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("get org monitoring settings: %w", err)
	}
	return Settings{ClientAbsentDays: int(days)}, true, nil
}

// Put implements Store.
func (s *SQLStore) Put(ctx context.Context, orgID uuid.UUID, st Settings) error {
	// The service's own check, repeated here only because it is what makes
	// the narrowing to int32 below safe.
	if err := st.validate(); err != nil {
		return err
	}
	if err := s.q.UpsertOrgMonitoringSettings(ctx, gen.UpsertOrgMonitoringSettingsParams{
		OrgID: orgID, ClientAbsentDays: int32(st.ClientAbsentDays),
	}); err != nil {
		return fmt.Errorf("store org monitoring settings: %w", err)
	}
	return nil
}

// ClientAbsentDays is the evaluator's read: the organization's threshold
// with the default applied (no row reads as DefaultClientAbsentDays; 0 =
// off). It works inside an organization's scope and outside any.
func (s *SQLStore) ClientAbsentDays(ctx context.Context, orgID uuid.UUID) (int, error) {
	days, err := s.q.GetOrgClientAbsentDays(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("get client absent days: %w", err)
	}
	return int(days), nil
}
