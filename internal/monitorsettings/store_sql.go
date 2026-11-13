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
	row, err := s.q.GetOrgMonitoringSettings(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("get org monitoring settings: %w", err)
	}
	return settingsOf(row.ClientAbsentDays, row.RetentionDays), true, nil
}

// Put implements Store: one upsert that replaces client_absent_days and
// replaces the retention choice only when u carries one (0 stores NULL).
func (s *SQLStore) Put(ctx context.Context, orgID uuid.UUID, u Update) (Settings, error) {
	// The service's own check, repeated here only because it is what makes
	// the narrowing to int32 below safe.
	if err := u.validate(); err != nil {
		return Settings{}, err
	}
	params := gen.UpsertOrgMonitoringSettingsParams{
		OrgID: orgID, ClientAbsentDays: int32(u.ClientAbsentDays), SetRetention: u.RetentionDays != nil,
	}
	if u.RetentionDays != nil && *u.RetentionDays != 0 {
		days := int32(*u.RetentionDays)
		params.RetentionDays = &days
	}
	row, err := s.q.UpsertOrgMonitoringSettings(ctx, params)
	if err != nil {
		return Settings{}, fmt.Errorf("store org monitoring settings: %w", err)
	}
	return settingsOf(row.ClientAbsentDays, row.RetentionDays), nil
}

// settingsOf converts a stored row; a NULL retention_days is no choice (0).
func settingsOf(clientAbsentDays int32, retentionDays *int32) Settings {
	st := Settings{ClientAbsentDays: int(clientAbsentDays)}
	if retentionDays != nil {
		st.RetentionDays = int(*retentionDays)
	}
	return st
}
