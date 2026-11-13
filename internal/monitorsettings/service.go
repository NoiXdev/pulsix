// Package monitorsettings holds an organization's monitoring settings:
// client_absent_days (backlog 3.57), after how many days without contact a
// client host's agent raises the "not seen since …" warning, and the
// retention choice (backlog 3.15), how many days the organization keeps
// its measurements and state history. The settings are stored on the
// org-keyed org_monitoring_settings table (migrations 00091, 00093); an
// organization without a row has every setting at its default.
//
// The retention choice never exceeds the instance maximum
// (PULSIX_SAMPLE_RETENTION), which the service is given; the Purger
// (retention_purge.go) deletes the rows an organization with a shorter
// effective retention no longer keeps.
//
// The service is the API's read and write; the evaluation ticker reads the
// threshold with the default applied through the generated
// GetOrgClientAbsentDays query (internal/rules' Load), outside any
// organization scope.
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

// Settings is an organization's monitoring settings as stored.
type Settings struct {
	// ClientAbsentDays is after how many days without contact a client
	// host's agent raises the absent warning; 0 = never.
	ClientAbsentDays int
	// RetentionDays is the organization's retention choice in days (30,
	// 90 or 365, ValidRetentionDays); 0 means no choice (the instance
	// maximum).
	RetentionDays int
}

// Update is a write of the settings. ClientAbsentDays is always replaced.
// RetentionDays nil keeps the stored retention choice; a non-nil value
// replaces it, 0 clearing it back to the instance maximum.
type Update struct {
	ClientAbsentDays int
	RetentionDays    *int
}

// View is the settings with the instance maximum and the retention that
// applies: what the API returns.
type View struct {
	Settings
	// RetentionMaxDays is the instance maximum (PULSIX_SAMPLE_RETENTION).
	RetentionMaxDays int
	// RetentionEffectiveDays is EffectiveRetentionDays of the choice and
	// the maximum: what the Purger and TimescaleDB keep.
	RetentionEffectiveDays int
}

// validate is the one range check of a write: the service refuses with
// it, and the SQL store applies it before narrowing to the columns'
// integer type. The table's CHECKs are the same bounds for other writers.
func (u Update) validate() error {
	if u.ClientAbsentDays < 0 || u.ClientAbsentDays > MaxClientAbsentDays {
		return fmt.Errorf("%w: client_absent_days must be between 0 and %d", ErrInvalidInput, MaxClientAbsentDays)
	}
	if u.RetentionDays != nil && *u.RetentionDays != 0 && !ValidRetentionDays(*u.RetentionDays) {
		return fmt.Errorf("%w: retention_days must be 30, 90 or 365, or null for the instance default", ErrInvalidInput)
	}
	return nil
}

// Defaults is what an organization without a stored row gets.
func Defaults() Settings {
	return Settings{ClientAbsentDays: DefaultClientAbsentDays}
}

// Store persists the settings. Get reports ok false when the organization
// has no row (the defaults apply); Put applies an Update (creating the
// row when there is none) and returns the row as stored.
type Store interface {
	Get(ctx context.Context, orgID uuid.UUID) (Settings, bool, error)
	Put(ctx context.Context, orgID uuid.UUID, u Update) (Settings, error)
}

// Service reads and validates an organization's monitoring settings.
type Service struct {
	store            Store
	retentionMaxDays int
}

// NewService wires the store and the instance's retention maximum in days
// (config.Config.RetentionMaxDays).
func NewService(store Store, retentionMaxDays int) *Service {
	return &Service{store: store, retentionMaxDays: retentionMaxDays}
}

// Get returns the organization's settings, the defaults when none are
// stored.
func (s *Service) Get(ctx context.Context, orgID uuid.UUID) (View, error) {
	st, ok, err := s.store.Get(ctx, orgID)
	if err != nil {
		return View{}, fmt.Errorf("get monitoring settings: %w", err)
	}
	if !ok {
		st = Defaults()
	}
	return s.view(st), nil
}

// Set validates and applies an update and returns the settings as stored.
// An out-of-range value is ErrInvalidInput and writes nothing. A retention
// choice above the instance maximum is stored; the maximum caps it
// (RetentionEffectiveDays), so lowering PULSIX_SAMPLE_RETENTION needs no
// rewrite of the stored choices.
func (s *Service) Set(ctx context.Context, orgID uuid.UUID, u Update) (View, error) {
	if err := u.validate(); err != nil {
		return View{}, err
	}
	st, err := s.store.Put(ctx, orgID, u)
	if err != nil {
		return View{}, fmt.Errorf("store monitoring settings: %w", err)
	}
	return s.view(st), nil
}

func (s *Service) view(st Settings) View {
	return View{
		Settings:               st,
		RetentionMaxDays:       s.retentionMaxDays,
		RetentionEffectiveDays: EffectiveRetentionDays(st.RetentionDays, s.retentionMaxDays),
	}
}
