package tenancy

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PendingInvite is an invitation that can still be accepted: unaccepted,
// unexpired, and sent by someone who may still add members. Listed with
// expired ones included, Expired marks those past ExpiresAt.
type PendingInvite struct {
	ID            uuid.UUID
	Email         string
	Role          Role
	InvitedBy     uuid.UUID
	InvitedByName string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	Expired       bool
}

// ExpiredInviteListWindow is how long an expired invitation stays in the
// list asked for with the expired ones: it can be resent for that long
// after it lapsed, and then drops out, so an organization's list does not
// keep every invitation nobody accepted. The InviteReaper (backlog 3.128)
// then deletes the rows that fell out of it.
const ExpiredInviteListWindow = 30 * 24 * time.Hour

// PendingInvites lists orgID's open invitations (backlog 7.29). An
// invitation is a standing offer of a membership, so the people who manage
// members have to be able to see what is outstanding — before this, an
// invitation sent to a wrong address could only be waited out.
// includeExpired adds the invitations that are open but for their expiry
// and expired within ExpiredInviteListWindow, each marked Expired against
// the service's clock, so they can be resent (spec 2026-11-19 §4).
func (s *Service) PendingInvites(ctx context.Context, orgID uuid.UUID, includeExpired bool) ([]PendingInvite, error) {
	now := s.now()
	expiresAfter := now
	if includeExpired {
		expiresAfter = now.Add(-ExpiredInviteListWindow)
	}
	invites, err := s.store.ListPendingInvites(ctx, orgID, expiresAfter)
	if err != nil {
		return nil, fmt.Errorf("list pending invites: %w", err)
	}
	for i := range invites {
		invites[i].Expired = !invites[i].ExpiresAt.After(now)
	}
	return invites, nil
}

// RevokeInvite withdraws one of orgID's open invitations, so its link stops
// working (backlog 7.29). It returns ErrNotFound when orgID has no open
// invitation with that id, including one already accepted.
func (s *Service) RevokeInvite(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.RevokeInvite(ctx, orgID, id)
}
