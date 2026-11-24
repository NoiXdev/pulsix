package runrequests

import (
	"context"

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
