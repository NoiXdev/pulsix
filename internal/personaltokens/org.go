package personaltokens

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// OrgEntry is one personal token's grant as an organization's admins see it
// on their token page (spec rule 6): whose token, its cap there, whether
// they blocked it, and its last use there. CreatedAt and ExpiresAt are the
// token's own.
type OrgEntry struct {
	TokenID               uuid.UUID
	Name                  string
	Kind                  auth.TokenKind
	OwnerID               uuid.UUID
	OwnerName, OwnerEmail string
	Role                  tenancy.Role
	Blocked               bool
	TokenRevoked          bool // listed only while its grant here is blocked
	// OwnerBlocked: an admin blocked a grant of the owner here, this one or
	// another token's, so every token of theirs is refused here.
	OwnerBlocked bool
	LastUsedAt   *time.Time
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

// The organization methods run with the request's database scope bound to
// orgID, so row-level security confines every grant they read or write to
// that organization (controller ruling P7).

// ListForOrg lists orgID's grants of personal tokens that are not
// revoked, and every blocked grant — of revoked tokens too, since a block
// keeps its owner out of the organization until an admin lifts it.
func (s *Service) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]OrgEntry, error) {
	entries, err := s.store.OrgEntries(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list personal token grants: %w", err)
	}
	return entries, nil
}

// RevokeGrant is an organization admin's revocation of tokenID's grant for
// orgID. The grant is blocked, not deleted, and the block is per person and
// organization: no token of its owner gets a grant in orgID again until an
// admin lifts it (spec, "an admin's revocation sticks"). Blocking is one
// statement, so two admins racing get one success and one ErrBlocked. It
// returns the entry as blocked, with the cap it blocked.
func (s *Service) RevokeGrant(ctx context.Context, orgID, tokenID, adminID uuid.UUID) (OrgEntry, error) {
	e, err := s.store.BlockGrant(ctx, tokenID, orgID, adminID)
	if !errors.Is(err, ErrNotFound) {
		return e, err
	}
	cur, readErr := s.store.OrgEntry(ctx, orgID, tokenID)
	if readErr == nil && cur.Blocked {
		return OrgEntry{}, ErrBlocked
	}
	if readErr != nil && !errors.Is(readErr, ErrNotFound) {
		return OrgEntry{}, readErr
	}
	return OrgEntry{}, ErrNotFound
}

// UnblockGrant lifts the block of tokenID's owner in orgID: every blocked
// grant of theirs there is deleted, so no token of theirs holds a grant for
// orgID until they add one again. tokenID names any entry of the blocked
// person (blocked itself or only through the person); an entry of a person
// who is not blocked is ErrNotFound. It returns the entry as it was.
func (s *Service) UnblockGrant(ctx context.Context, orgID, tokenID uuid.UUID) (OrgEntry, error) {
	e, err := s.store.OrgEntry(ctx, orgID, tokenID)
	if err != nil {
		return OrgEntry{}, err
	}
	if !e.Blocked && !e.OwnerBlocked {
		return OrgEntry{}, ErrNotFound
	}
	if err := s.store.UnblockOwner(ctx, orgID, e.OwnerID); err != nil {
		return OrgEntry{}, err
	}
	return e, nil
}

// Allowed reads orgID's switch "Allow personal tokens" (on by default).
func (s *Service) Allowed(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return s.store.Allowed(ctx, orgID)
}

// SetAllowed turns orgID's switch on or off. Off, no grant can be added or
// raised there and the existing grants stop working at once; on again,
// every grant an admin did not block works again.
func (s *Service) SetAllowed(ctx context.Context, orgID uuid.UUID, on bool) error {
	return s.store.SetAllowed(ctx, orgID, on)
}

// GrantCount counts tokenID's unblocked grants; orgID is the one
// organization when n == 1 (the assistant endpoint's single-grant rule,
// spec rule 7). It reads across organizations, so it runs at instance
// level; bound to an organization it fails closed.
func (s *Service) GrantCount(ctx context.Context, tokenID uuid.UUID) (orgID uuid.UUID, n int, err error) {
	return s.store.LiveGrants(ctx, tokenID)
}
