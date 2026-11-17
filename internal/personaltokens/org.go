package personaltokens

import (
	"context"
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
	LastUsedAt            *time.Time
	CreatedAt             time.Time
	ExpiresAt             *time.Time
}

// The organization methods run with the request's database scope bound to
// orgID, so row-level security confines every grant they read or write to
// that organization (controller ruling P7).

// ListForOrg lists orgID's grants of personal tokens that are not revoked,
// blocked ones included so an admin can lift the block.
func (s *Service) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]OrgEntry, error) {
	entries, err := s.store.OrgEntries(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list personal token grants: %w", err)
	}
	return entries, nil
}

// RevokeGrant is an organization admin's revocation of tokenID's grant for
// orgID: the grant is blocked, not deleted, so the owner cannot add the
// organization again until an admin lifts the block (spec, "an admin's
// revocation sticks"). An already blocked grant is ErrBlocked. It returns
// the entry as it is afterwards.
func (s *Service) RevokeGrant(ctx context.Context, orgID, tokenID, adminID uuid.UUID) (OrgEntry, error) {
	e, err := s.store.OrgEntry(ctx, orgID, tokenID)
	if err != nil {
		return OrgEntry{}, err
	}
	if e.Blocked {
		return OrgEntry{}, ErrBlocked
	}
	if err := s.store.BlockGrant(ctx, tokenID, orgID, adminID); err != nil {
		return OrgEntry{}, err
	}
	e.Blocked = true
	return e, nil
}

// UnblockGrant lifts an admin's block: the blocked grant is deleted, so
// the token holds no grant for orgID until its owner adds one again. A
// grant that is not blocked is ErrNotFound. It returns the entry as it
// was.
func (s *Service) UnblockGrant(ctx context.Context, orgID, tokenID uuid.UUID) (OrgEntry, error) {
	e, err := s.store.OrgEntry(ctx, orgID, tokenID)
	if err != nil {
		return OrgEntry{}, err
	}
	if !e.Blocked {
		return OrgEntry{}, ErrNotFound
	}
	if err := s.store.UnblockGrant(ctx, tokenID, orgID); err != nil {
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
// spec rule 7). It reads across organizations, so it runs at instance level.
func (s *Service) GrantCount(ctx context.Context, tokenID uuid.UUID) (orgID uuid.UUID, n int, err error) {
	return s.store.LiveGrants(ctx, tokenID)
}
