package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// workerJobRequeuer adapts the job queue to workers.JobRequeuer with the
// configured attempt limit (PULSIX_JOB_MAX_ATTEMPTS).
type workerJobRequeuer struct {
	q           *jobs.SQLQueue
	maxAttempts int
}

func (a workerJobRequeuer) RequeueWorkerJobs(ctx context.Context, orgID, workerID uuid.UUID, reason string) (int, int, error) {
	return a.q.RequeueWorkerJobs(ctx, orgID, workerID, reason, a.maxAttempts)
}
