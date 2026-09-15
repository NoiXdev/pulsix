// The executor dispatch: v0.2b's worker runs one job at a time and routes
// each claimed job to the executor its kind belongs to. The ansible-runner
// executor (internal/worker/runner) serves run and ad-hoc jobs; the SSH
// executors (Tasks 10–11) join through the same map, and a kind with no
// executor in this build reports an infrastructure error instead of
// pretending to have run — the claim is at-least-once, the manager's run
// view shows the reason.
package worker

import (
	"context"
	"fmt"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// CompositeExecutor routes claimed jobs by kind to their executors.
type CompositeExecutor struct {
	byKind map[string]Executor
}

// NewCompositeExecutor wires the kind dispatch. Kinds absent from the map
// report an executor error (Tasks 10–11 add their executors via the same
// file's wiring).
func NewCompositeExecutor(byKind map[string]Executor) *CompositeExecutor {
	return &CompositeExecutor{byKind: byKind}
}

// Run runs the job with the executor its kind belongs to.
func (c *CompositeExecutor) Run(ctx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	exec, ok := c.byKind[job.GetKind()]
	if !ok {
		return StatusError, nil, fmt.Errorf("job kind %q has no executor in this build", job.GetKind())
	}
	return exec.Run(ctx, job, env, sink)
}
