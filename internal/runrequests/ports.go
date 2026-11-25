package runrequests

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// Pictures resolves what a request would run now.
type Pictures interface {
	Picture(ctx context.Context, orgID uuid.UUID, r Request) (Picture, error)
}

// Enqueuer starts what an approval allows, through the existing run paths.
type Enqueuer interface {
	// Run and RetryFailed enqueue into the run group group; Run enqueues
	// the run request r asks for, with its host, overrides, pinned commit
	// and enroll list. RetryFailed pins the retry to approved.PinnedCommit
	// when its original job has none ("" for no pin), and stores
	// approved.EnrollHostIDs on its jobs.
	Run(ctx context.Context, orgID uuid.UUID, r Request, actor uuid.UUID, idempotencyKey string, group uuid.UUID, followUps []uuid.UUID) error
	// RetryFailed retries the failed hosts of r's job with r's overrides.
	RetryFailed(ctx context.Context, orgID uuid.UUID, r Request, actor uuid.UUID, idempotencyKey string, group uuid.UUID, followUps []uuid.UUID, approved jobs.ApprovedRun) error
}

// Validator checks a request the way the run and retry routes would check
// the run itself, so a request that could never run is refused early.
type Validator interface {
	ValidateRun(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, ov *Overrides) (revision int64, err error)
	// ValidateRetry also returns the run group of the job, whose request's
	// extra vars a retry repeats.
	ValidateRetry(ctx context.Context, orgID, jobID uuid.UUID) (templateID, runGroup uuid.UUID, revision int64, err error)
	// RetryRunOverrides checks the extra vars a retry of jobID repeats
	// against the template and the retry's hosts as they are now, and
	// returns them as the retry's run overrides; nil for none.
	RetryRunOverrides(ctx context.Context, orgID, jobID uuid.UUID, ov *Overrides) (*jobs.RunOverrides, error)
}

// Automation checks and applies the requests that create or change what
// runs later (spec 2026-11-25 assistant API gaps, C): schedules now,
// webhook triggers next.
type Automation interface {
	// ValidateSchedule checks a schedule request the way the schedule
	// routes would check the schedule itself, against target (the id a
	// create takes, or the schedule an update or resume names), and
	// returns what the request stores: the change, with an update's patch
	// merged into the schedule as it is now, and the template the
	// schedule runs with its revision.
	ValidateSchedule(ctx context.Context, orgID, target uuid.UUID, in ScheduleInput) (ScheduleChange, uuid.UUID, int64, error)
	// ApplySchedule does what the approved schedule request r asks for,
	// exactly as p (the approved picture) shows it, as actor.
	ApplySchedule(ctx context.Context, orgID uuid.UUID, r Request, p PictureSchedule, actor uuid.UUID) error
}
