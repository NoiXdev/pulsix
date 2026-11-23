package runrequests

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// PendingCount is the number of orgID's pending, unexpired requests: what
// the approvers' badge shows.
func (s *Service) PendingCount(ctx context.Context, orgID uuid.UUID) (int, error) {
	n, err := s.store.CountPending(ctx, orgID, nil, s.now())
	if err != nil {
		return 0, err
	}
	return n, nil
}

// checkPendingCap refuses a token's request while the token already has
// MaxPendingPerToken pending, unexpired ones; a decision or an expiry frees
// a slot. A request without a token is not capped. Two requests of one
// token racing each other can both pass the count, so the cap may be
// overshot by the requests in flight at that moment: it bounds the queue,
// it is not an exact quota.
func (s *Service) checkPendingCap(ctx context.Context, orgID uuid.UUID, token *uuid.UUID) error {
	if token == nil {
		return nil
	}
	n, err := s.store.CountPending(ctx, orgID, token, s.now())
	if err != nil {
		return fmt.Errorf("count the token's pending run requests: %w", err)
	}
	if n >= MaxPendingPerToken {
		return ErrTooManyPending
	}
	return nil
}
