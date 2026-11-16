package chain

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/noixdev/pulsix/internal/jobs"
)

// Sink resolves a finished run group's follow-ups. It runs inside the
// finishing transaction ctx carries, in a savepoint of its own, so a failure
// here never aborts the job's terminal write; the queue logs and drops the
// returned error.
type Sink struct {
	store *SQLStore
	now   func() time.Time
	log   *slog.Logger
}

var _ jobs.RunGroupSink = (*Sink)(nil)

// NewSink wires the sink; nil now and log take defaults.
func NewSink(store *SQLStore, now func() time.Time, log *slog.Logger) *Sink {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Sink{store: store, now: now, log: log}
}

// RunGroupFinished implements jobs.RunGroupSink.
func (s *Sink) RunGroupFinished(ctx context.Context, r jobs.RunGroupResult) error {
	if err := s.store.ResolveGroup(ctx, r.OrgID, r.GroupID, r.Status, s.now()); err != nil {
		return fmt.Errorf("resolve follow-ups of run group %s: %w", r.GroupID, err)
	}
	return nil
}
