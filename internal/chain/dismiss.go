package chain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// ErrRunFinished refuses dismissing the follow-ups of a run that has
// finished: they are decided already.
var ErrRunFinished = errors.New("this run has finished; its follow-ups can no longer be dismissed")

// reasonDismissed is the reason a dismissed follow-up records.
const reasonDismissed = "dismissed by %s"

// RunReader reads a job and the state of its run group (jobs.Service).
type RunReader interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (jobs.Job, error)
	GroupSummaries(ctx context.Context, orgID uuid.UUID, groupIDs []uuid.UUID) (map[uuid.UUID]jobs.GroupSummary, error)
}

// DismissStore cancels a run group's pending follow-ups (SQLStore).
type DismissStore interface {
	Dismiss(ctx context.Context, orgID, groupID uuid.UUID, reason string, now time.Time) (int, error)
}

var _ DismissStore = (*SQLStore)(nil)

// Dismisser dismisses the pending follow-ups of a run that is still active.
type Dismisser struct {
	runs  RunReader
	store DismissStore
	now   func() time.Time
}

// NewDismisser wires the dismisser; a nil now takes time.Now.
func NewDismisser(runs RunReader, store DismissStore, now func() time.Time) *Dismisser {
	if now == nil {
		now = time.Now
	}
	return &Dismisser{runs: runs, store: store, now: now}
}

// DismissFollowUps dismisses the pending follow-ups of jobID's run group on
// behalf of userName and returns how many. ErrRunFinished once the run has
// finished; the job lookup's errors (jobs.ErrNotFound) pass through.
func (d *Dismisser) DismissFollowUps(ctx context.Context, orgID, jobID uuid.UUID, userName string) (int, error) {
	j, err := d.runs.Get(ctx, orgID, jobID)
	if err != nil {
		return 0, err
	}
	sums, err := d.runs.GroupSummaries(ctx, orgID, []uuid.UUID{j.RunGroupID})
	if err != nil {
		return 0, fmt.Errorf("summarise the run: %w", err)
	}
	if sum, ok := sums[j.RunGroupID]; !ok || sum.Status.Terminal() {
		return 0, ErrRunFinished
	}
	return d.store.Dismiss(ctx, orgID, j.RunGroupID, fmt.Sprintf(reasonDismissed, userName), d.now())
}
