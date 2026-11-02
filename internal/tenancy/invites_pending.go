package tenancy

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PendingInvite is an invitation that can still be accepted: unaccepted,
// unexpired, and sent by someone who may still add members.
type PendingInvite struct {
	ID            uuid.UUID
	Email         string
	Role          Role
	InvitedBy     uuid.UUID
	InvitedByName string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

// PendingInvites lists orgID's open invitations (backlog 7.29). An
// invitation is a standing offer of a membership, so the people who manage
// members have to be able to see what is outstanding — before this, an
// invitation sent to a wrong address could only be waited out.
func (s *Service) PendingInvites(ctx context.Context, orgID uuid.UUID) ([]PendingInvite, error) {
	invites, err := s.store.ListPendingInvites(ctx, orgID, s.now())
	if err != nil {
		return nil, fmt.Errorf("list pending invites: %w", err)
	}
	return invites, nil
}

// RevokeInvite withdraws one of orgID's open invitations, so its link stops
// working (backlog 7.29). It returns ErrNotFound when orgID has no open
// invitation with that id, including one already accepted.
func (s *Service) RevokeInvite(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.RevokeInvite(ctx, orgID, id)
}
