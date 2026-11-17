// Package personaltokens holds a user's personal API and assistant tokens'
// grants (backlog 3.100, spec docs/superpowers/specs/2026-11-17-personal-
// tokens-design.md): a personal token acts only in the organizations granted
// on it, each with a role cap, never above the user's current membership
// role there. The token itself (its secret, kind, areas and expiry) is
// resolved by internal/auth; this package answers, per request, what the
// token may do in one organization.
package personaltokens

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

var (
	// ErrNotFound is the store's answer for a row that does not exist.
	ErrNotFound = errors.New("personal token not found")
	// ErrNoGrant: the token holds no live grant for the organization —
	// none at all, or one an organization admin revoked.
	ErrNoGrant = errors.New("the token holds no grant for this organization")
	// ErrSwitchedOff: the organization turned personal tokens off.
	ErrSwitchedOff = errors.New("personal tokens are turned off for this organization")
)

// Store is what the read side needs. Under an organization's database
// scope row-level security confines every grant read to that organization.
type Store interface {
	// Cap returns the grant of tokenID for orgID and whether an
	// organization admin blocked it; ErrNotFound when there is none.
	Cap(ctx context.Context, tokenID, orgID uuid.UUID) (role tenancy.Role, blocked bool, err error)
	// Allowed reads the organization's switch; no row means allowed.
	Allowed(ctx context.Context, orgID uuid.UUID) (bool, error)
	// TouchGrant records the grant's last use with the database's own
	// clock, at most once a minute.
	TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID) error
}

// Members reads a user's current membership role (tenancy.Service).
type Members interface {
	MembershipOf(ctx context.Context, orgID, userID uuid.UUID) (tenancy.Role, error)
}

// Service answers what a personal token may do in one organization.
type Service struct {
	store   Store
	members Members
}

// NewService wires the store and the membership lookup.
func NewService(store Store, members Members) *Service {
	return &Service{store: store, members: members}
}

// Cap is the role cap of tokenID's grant for orgID: no grant or a blocked
// one is ErrNoGrant, the organization's switch off is ErrSwitchedOff. Any
// other error is the store's and propagates, so the caller fails closed.
// The grant is checked first, so a token with no business in orgID learns
// nothing about its switch.
func (s *Service) Cap(ctx context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, error) {
	role, blocked, err := s.store.Cap(ctx, tokenID, orgID)
	if errors.Is(err, ErrNotFound) || (err == nil && blocked) {
		return "", ErrNoGrant
	}
	if err != nil {
		return "", fmt.Errorf("read grant: %w", err)
	}
	on, err := s.store.Allowed(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("read personal token switch: %w", err)
	}
	if !on {
		return "", ErrSwitchedOff
	}
	return role, nil
}

// EffectiveRole is what tokenID may do in orgID for its owner userID: the
// lower of the grant's cap and the user's current membership role (spec
// rule 2). Not a member is tenancy.ErrNotMember; otherwise Cap's errors.
func (s *Service) EffectiveRole(ctx context.Context, tokenID, orgID, userID uuid.UUID) (tenancy.Role, error) {
	member, err := s.members.MembershipOf(ctx, orgID, userID)
	if err != nil {
		return "", err
	}
	limit, err := s.Cap(ctx, tokenID, orgID)
	if err != nil {
		return "", err
	}
	return tenancy.Lower(member, limit), nil
}

// TouchGrant records the grant's use. It is bookkeeping, never an
// authorization decision: a failure is logged, not returned.
func (s *Service) TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID) {
	if err := s.store.TouchGrant(ctx, tokenID, orgID); err != nil {
		slog.ErrorContext(ctx, "personal token grant touch failed", "err", err, "token_id", tokenID, "org_id", orgID)
	}
}
