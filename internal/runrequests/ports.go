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
	// the run request r asks for, with its host, overrides and pinned
	// commit. RetryFailed pins the retry to pinnedCommit when its original
	// job has none ("" for no pin).
	Run(ctx context.Context, orgID uuid.UUID, r Request, actor uuid.UUID, idempotencyKey string, group uuid.UUID, followUps []uuid.UUID) error
	// RetryFailed retries the failed hosts of r's job with r's overrides.
	RetryFailed(ctx context.Context, orgID uuid.UUID, r Request, actor uuid.UUID, idempotencyKey string, group uuid.UUID, followUps []uuid.UUID, pinnedCommit string) error
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
