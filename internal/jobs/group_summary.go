package jobs

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// GroupSummary is one run group at a glance, for the calendar and the
// weekly digest: its first job (the run view link), an aggregate status
// and its time span.
type GroupSummary struct {
	GroupID, FirstJobID uuid.UUID
	// Status is queued while every job waits, running while any job is
	// not terminal, and otherwise the RunGroupResult aggregate (failed,
	// cancelled, succeeded).
	Status                Status
	StartedAt, FinishedAt *time.Time
}

// Duration is FinishedAt - StartedAt for a finished group, zero otherwise.
func (g GroupSummary) Duration() time.Duration {
	if g.StartedAt == nil || g.FinishedAt == nil || !g.Status.Terminal() {
		return 0
	}
	return g.FinishedAt.Sub(*g.StartedAt)
}

// GroupSummaries summarises the given run groups of the organization.
func (s *Service) GroupSummaries(ctx context.Context, orgID uuid.UUID, groupIDs []uuid.UUID) (map[uuid.UUID]GroupSummary, error) {
	if len(groupIDs) == 0 {
		return map[uuid.UUID]GroupSummary{}, nil
	}
	return s.store.GroupSummaries(ctx, orgID, groupIDs)
}
