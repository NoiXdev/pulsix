package tenancy

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ResendInvite sends orgID's invitation id again (spec 2026-11-19 §4): it
// gets a fresh link, so the one sent before stops working, its expiry
// starts over at InviteTTL from now, and byUserID becomes its issuer. The
// invitation carries its issuer's authority to add members (backlog 7.29),
// so a resend by someone who holds it renews an invitation whose original
// sender has since lost it. An expired invitation can be resent; an
// accepted or revoked one cannot (ErrNotFound), nor can another
// organization's.
//
// Mail is handled like Invite's: with no outgoing mail configured the new
// link comes back in AcceptURL. When configured delivery fails the
// invitation is put back as it was, old link, expiry and issuer included,
// and the call fails with ErrInviteDelivery: nobody was told about the new
// link, so it must not have replaced the one they have.
func (s *Service) ResendInvite(ctx context.Context, orgID, byUserID, id uuid.UUID) (Invite, error) {
	org, err := s.store.GetOrgByID(ctx, orgID)
	if err != nil {
		return Invite{}, err
	}
	token, sum, err := newInviteToken()
	if err != nil {
		return Invite{}, err
	}
	inv, prev, err := s.store.RotateInvite(ctx, orgID, id, InviteRotation{
		TokenHash: sum, ExpiresAt: s.now().Add(InviteTTL), InvitedBy: byUserID,
	})
	if err != nil {
		return Invite{}, fmt.Errorf("rotate invite: %w", err)
	}
	inv.Token = token
	if sendErr := s.mailInvite(ctx, org.Name, &inv); sendErr != nil {
		if err := s.store.RestoreInvite(ctx, orgID, id, sum, prev); err != nil {
			return Invite{}, fmt.Errorf("%w: %w (and restoring the invitation failed: %w)", ErrInviteDelivery, sendErr, err)
		}
		return Invite{}, fmt.Errorf("%w: %w", ErrInviteDelivery, sendErr)
	}
	return inv, nil
}
