// Package monitorsettings holds an organization's monitoring settings
// (backlog 3.57): today client_absent_days, after how many days without
// contact a client host's agent raises the "not seen for N days" warning.
// The settings are stored on the org-keyed org_monitoring_settings table
// (migration 00091); an organization without a row has every setting at
// its default. Backlog 3.15 adds its retention setting here.
//
// The service is the API's read and write; the evaluation ticker reads the
// threshold with the default applied through SQLStore.ClientAbsentDays (or
// the generated GetOrgClientAbsentDays query it wraps).
package monitorsettings

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const (
	// DefaultClientAbsentDays is client_absent_days for an organization
	// without a stored row. It must equal the column default (00091) and
	// the COALESCE in GetOrgClientAbsentDays; a store test pins the three.
	DefaultClientAbsentDays = 14
	// MaxClientAbsentDays bounds client_absent_days; 0 turns the warning
	// off. The table's CHECK enforces the same range.
	MaxClientAbsentDays = 365
)

// ErrInvalidInput means a setting is out of its range.
var ErrInvalidInput = errors.New("invalid input")

// Settings is an organization's monitoring settings.
type Settings struct {
	// ClientAbsentDays is after how many days without contact a client
	// host's agent raises the absent warning; 0 = never.
	ClientAbsentDays int
}

// validate is the one range check of the settings: the service refuses
// with it, and the SQL store applies it before narrowing to the column's
// integer type. The table's CHECK is the same bound for other writers.
func (st Settings) validate() error {
	if st.ClientAbsentDays < 0 || st.ClientAbsentDays > MaxClientAbsentDays {
		return fmt.Errorf("%w: client_absent_days must be between 0 and %d", ErrInvalidInput, MaxClientAbsentDays)
	}
	return nil
}

// Defaults is what an organization without a stored row gets.
func Defaults() Settings {
	return Settings{ClientAbsentDays: DefaultClientAbsentDays}
}

// Store persists the settings. Get reports ok false when the organization
// has no row (the defaults apply); Put replaces the row.
type Store interface {
	Get(ctx context.Context, orgID uuid.UUID) (Settings, bool, error)
	Put(ctx context.Context, orgID uuid.UUID, st Settings) error
}

// Service reads and validates an organization's monitoring settings.
type Service struct {
	store Store
}

// NewService wires the store.
func NewService(store Store) *Service { return &Service{store: store} }

// Get returns the organization's settings, the defaults when none are
// stored.
func (s *Service) Get(ctx context.Context, orgID uuid.UUID) (Settings, error) {
	st, ok, err := s.store.Get(ctx, orgID)
	if err != nil {
		return Settings{}, fmt.Errorf("get monitoring settings: %w", err)
	}
	if !ok {
		return Defaults(), nil
	}
	return st, nil
}

// Set validates and replaces the organization's settings and returns them
// as stored. An out-of-range value is ErrInvalidInput and writes nothing.
func (s *Service) Set(ctx context.Context, orgID uuid.UUID, st Settings) (Settings, error) {
	if err := st.validate(); err != nil {
		return Settings{}, err
	}
	if err := s.store.Put(ctx, orgID, st); err != nil {
		return Settings{}, fmt.Errorf("store monitoring settings: %w", err)
	}
	return st, nil
}
