// Package assistant holds the per-organization assistant access switch
// (spec docs/superpowers/specs/2026-11-16-assistant-access-design.md,
// "Assistant access switch"): while it is off, the assistant endpoint
// refuses every assistant token of the organization.
package assistant

import (
	"context"

	"github.com/google/uuid"
)

// Store persists the switch.
type Store interface {
	// Enabled reports the stored switch; no stored row means off.
	Enabled(ctx context.Context, orgID uuid.UUID) (bool, error)
	SetEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error
	// StartsFollowUps reports whether an approved run request also starts the
	// template's follow-ups; no stored row means off.
	StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error)
	SetStartsFollowUps(ctx context.Context, orgID uuid.UUID, on bool) error
}

// Service reads and writes the switch.
type Service struct{ store Store }

// NewService wires the store.
func NewService(store Store) *Service { return &Service{store: store} }

// Enabled reports whether assistant access is on for orgID.
func (s *Service) Enabled(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return s.store.Enabled(ctx, orgID)
}

// SetEnabled turns assistant access on or off for orgID.
func (s *Service) SetEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error {
	return s.store.SetEnabled(ctx, orgID, enabled)
}

// StartsFollowUps reports whether an approved run request of orgID also
// starts the template's follow-ups.
func (s *Service) StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return s.store.StartsFollowUps(ctx, orgID)
}

// SetStartsFollowUps sets whether approved run requests start follow-ups.
func (s *Service) SetStartsFollowUps(ctx context.Context, orgID uuid.UUID, on bool) error {
	return s.store.SetStartsFollowUps(ctx, orgID, on)
}
