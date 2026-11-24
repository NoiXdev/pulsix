// Package assistant holds the per-organization assistant access switch
// (spec docs/superpowers/specs/2026-11-16-assistant-access-design.md,
// "Assistant access switch"): while it is off, the assistant endpoint
// refuses every assistant token of the organization.
package assistant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Settings is an organization's assistant settings; an organization that
// never stored any has both off.
type Settings struct {
	// Enabled is the assistant access switch.
	Enabled bool
	// StartsFollowUps is whether an approved run request also starts the
	// template's follow-ups.
	StartsFollowUps bool
}

// Change is a partial update of Settings: a nil field keeps the stored
// value.
type Change struct {
	Enabled         *bool
	StartsFollowUps *bool
}

// ErrNothingToChange refuses a Change that names no field.
var ErrNothingToChange = errors.New("nothing to change: give assistant_enabled, approved_runs_start_follow_ups or both")

// Store persists the settings.
type Store interface {
	// Enabled reports the stored switch; no stored row means off.
	Enabled(ctx context.Context, orgID uuid.UUID) (bool, error)
	// StartsFollowUps reports whether an approved run request also starts the
	// template's follow-ups; no stored row means off.
	StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error)
	// InTx runs fn in one write transaction scoped to orgID that Lock and
	// Put join through the context fn receives.
	InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
	// Lock reads orgID's settings and locks them until the transaction
	// ends; an organization without a stored row gets one with both off.
	Lock(ctx context.Context, orgID uuid.UUID) (Settings, error)
	// Put stores orgID's settings over the row Lock locked.
	Put(ctx context.Context, orgID uuid.UUID, s Settings) error
}

// Service reads and writes the settings.
type Service struct{ store Store }

// NewService wires the store.
func NewService(store Store) *Service { return &Service{store: store} }

// Enabled reports whether assistant access is on for orgID.
func (s *Service) Enabled(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return s.store.Enabled(ctx, orgID)
}

// StartsFollowUps reports whether an approved run request of orgID also
// starts the template's follow-ups.
func (s *Service) StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return s.store.StartsFollowUps(ctx, orgID)
}

// Update applies c to orgID's settings in one transaction over the locked
// row, so two concurrent updates that each change one field both land, and
// reports the settings before and after. A Change naming no field is
// ErrNothingToChange.
func (s *Service) Update(ctx context.Context, orgID uuid.UUID, c Change) (before, after Settings, err error) {
	if c.Enabled == nil && c.StartsFollowUps == nil {
		return Settings{}, Settings{}, ErrNothingToChange
	}
	err = s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		cur, err := s.store.Lock(ctx, orgID)
		if err != nil {
			return err
		}
		next := cur
		if c.Enabled != nil {
			next.Enabled = *c.Enabled
		}
		if c.StartsFollowUps != nil {
			next.StartsFollowUps = *c.StartsFollowUps
		}
		if err := s.store.Put(ctx, orgID, next); err != nil {
			return err
		}
		before, after = cur, next
		return nil
	})
	if err != nil {
		return Settings{}, Settings{}, fmt.Errorf("update assistant settings: %w", err)
	}
	return before, after, nil
}
