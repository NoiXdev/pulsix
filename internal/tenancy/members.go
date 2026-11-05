package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ChangeRole sets target's role in orgID. actorRole is the caller's already
// -authorized effective role — api.requireOrg computes it as the lower of
// the caller's real membership role and any bearer token's own scoped role,
// and it is what actually decides the owner-only rules below. It must never
// be re-derived from the store here: a token minted at, say, admin can
// authenticate as its owner (an org owner) and would resolve to RoleOwner
// on a fresh store lookup, silently discarding the cap requireOrg computed
// and letting an admin-scoped token grant or revoke ownership (Finding 1,
// Task 10 fix round 1) — exactly the bypass api/authz.go's own doc comment
// says a token must never be able to produce.
//
// Only an actor with RoleOwner may grant or revoke the owner role, and the
// organization must always keep at least one owner: demoting the last
// owner fails with ErrLastOwner, enforced atomically by the store itself
// (see Store.UpdateMembershipRole) so two concurrent demotions of the same
// two owners can never both succeed (Finding 3, Task 10 fix round 1) — this
// no longer calls CountOwners at all.
func (s *Service) ChangeRole(ctx context.Context, orgID uuid.UUID, actorRole Role, actor, target uuid.UUID, role Role) error {
	if !Valid(role) {
		return ErrForbidden
	}
	current, err := s.store.GetMembership(ctx, orgID, target)
	if err != nil {
		return err // ErrNotFound when target genuinely isn't a member; propagated unchanged otherwise
	}
	if (role == RoleOwner || current == RoleOwner) && actorRole != RoleOwner {
		return ErrForbidden
	}
	if err := s.store.UpdateMembershipRole(ctx, orgID, target, role); err != nil {
		if errors.Is(err, ErrNotFound) && current == RoleOwner && role != RoleOwner {
			// GetMembership above just confirmed target exists, so the only
			// way the guarded UPDATE can have matched no row is its own
			// atomic last-owner check.
			return ErrLastOwner
		}
		return err
	}
	return nil
}

// RemoveMember removes target from orgID, with the same actor-role and
// last-owner handling as ChangeRole: actorRole is the caller's already-
// capped effective role, never re-derived from the store; removing the
// only remaining owner fails with ErrLastOwner, enforced atomically by the
// store (Store.DeleteMembership); and only an actor with RoleOwner may
// remove another owner.
func (s *Service) RemoveMember(ctx context.Context, orgID uuid.UUID, actorRole Role, actor, target uuid.UUID) error {
	current, err := s.store.GetMembership(ctx, orgID, target)
	if err != nil {
		return err // ErrNotFound when target genuinely isn't a member; propagated unchanged otherwise
	}
	if current == RoleOwner && actorRole != RoleOwner {
		return ErrForbidden
	}
	if err := s.store.DeleteMembership(ctx, orgID, target); err != nil {
		if errors.Is(err, ErrNotFound) && current == RoleOwner {
			// Same reasoning as ChangeRole above: existence was just
			// confirmed, so a guarded delete matching no row means the
			// atomic last-owner check refused it.
			return ErrLastOwner
		}
		return err
	}
	return nil
}

// DeactivateUser disables target's account instance-wide and ends its
// access immediately: sessions and API tokens are revoked, not merely
// flagged for later. It refuses the caller's own account
// (ErrNotOnOwnAccount, backlog 7.74), and it refuses, with
// ErrLastInstanceAdmin and before revoking anything, to disable the last
// active instance administrator, since the instance could then only be
// recovered by editing the database. With the self refusal in front, an
// administrator meets that rule only when two of them deactivate each
// other at once; the store's guard is atomic for exactly that case.
// actorIsInstanceAdmin is supplied by the handler from the caller's
// auth.Principal — the tenancy package has no notion of instance-level
// roles of its own, only org membership, so it cannot look this up itself.
//
// wasActive reports whether the account was in use right before: not yet
// deactivated, and with its invitation accepted (it has a password). Only
// then is its person told (accounts.Service.NotifyDeactivated).
// Deactivating an account that is already deactivated keeps its first
// deactivation, revokes again, and reports false.
func (s *Service) DeactivateUser(ctx context.Context, actorIsInstanceAdmin bool, actor, target uuid.UUID) (wasActive bool, err error) {
	if !actorIsInstanceAdmin {
		return false, ErrForbidden
	}
	if actor == target {
		return false, ErrNotOnOwnAccount
	}
	wasActive, err = s.store.DisableUser(ctx, target, s.now())
	if err != nil {
		return false, fmt.Errorf("disable user: %w", err)
	}
	if err := s.store.RevokeSessionsForUser(ctx, target); err != nil {
		return false, fmt.Errorf("revoke sessions: %w", err)
	}
	if err := s.store.RevokeTokensForUser(ctx, target); err != nil {
		return false, fmt.Errorf("revoke tokens: %w", err)
	}
	return wasActive, nil
}
