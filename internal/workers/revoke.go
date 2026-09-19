package workers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotRevoked refuses deleting a worker that was not revoked first.
var ErrNotRevoked = errors.New("worker is not revoked")

// OnlineWindow: a worker seen within it counts as online (spec §4).
const OnlineWindow = 2 * time.Minute

// StreamCloser ends a worker's open event streams (grpcsrv.StreamRegistry).
// CloseWorker returns only once none of them can store another event, so
// the requeue that follows never gets a stray event in the job's log.
type StreamCloser interface {
	CloseWorker(workerID uuid.UUID) int
}

// JobRequeuer hands a worker's jobs back to the queue (the job queue with
// the configured attempt limit, adapted in internal/app).
type JobRequeuer interface {
	RequeueWorkerJobs(ctx context.Context, orgID, workerID uuid.UUID, reason string) (requeued, ended int, err error)
}

// RevokeResult reports what a revocation cut.
type RevokeResult struct {
	Streams  int
	Requeued int
	Ended    int
}

// View is one row of the Workers page.
type View struct {
	Worker
	ZoneSlug      string
	Online        bool
	CertExpiresAt *time.Time
	CurrentJobID  *uuid.UUID
}

// ViewDetails are the per-worker facts the list joins in.
type ViewDetails struct {
	Expiry     map[uuid.UUID]time.Time
	CurrentJob map[uuid.UUID]uuid.UUID
}

// SetRevokeHooks wires the stream registry and the job requeue a
// revocation triggers (post-construction, like the job queue's sinks).
func (s *Service) SetRevokeHooks(streams StreamCloser, jobs JobRequeuer) {
	s.streams, s.jobs = streams, jobs
}

// Revoke refuses every certificate of the worker from now on and marks it
// revoked (the row stays for audit), then ends its open event streams and
// requeues its jobs (spec §4 "Revocation"). Revoking twice is harmless.
func (s *Service) Revoke(ctx context.Context, orgID, id uuid.UUID) (Worker, RevokeResult, error) {
	w, err := s.store.Revoke(ctx, orgID, id)
	if err != nil {
		return Worker{}, RevokeResult{}, err
	}
	var res RevokeResult
	if s.streams != nil {
		res.Streams = s.streams.CloseWorker(id)
	}
	if s.jobs != nil {
		if res.Requeued, res.Ended, err = s.jobs.RequeueWorkerJobs(ctx, orgID, id, "worker revoked"); err != nil {
			return w, res, fmt.Errorf("requeue the revoked worker's jobs: %w", err)
		}
	}
	return w, res, nil
}

// Delete removes a revoked worker's row and certificates; an active worker
// must be revoked first.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	w, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if w.Status != StatusRevoked {
		return ErrNotRevoked
	}
	return s.store.Delete(ctx, orgID, id)
}

// ListView is the Workers page: every worker with its zone, online state
// (seen within OnlineWindow), certificate expiry and current job.
func (s *Service) ListView(ctx context.Context, orgID uuid.UUID) ([]View, error) {
	list, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	details, err := s.store.ViewDetails(ctx, orgID)
	if err != nil {
		return nil, err
	}
	slugs := map[uuid.UUID]string{}
	if s.zones != nil {
		zones, err := s.zones.ListZones(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("list zones: %w", err)
		}
		for _, z := range zones {
			slugs[z.ID] = z.Slug
		}
	}
	now := s.now()
	out := make([]View, len(list))
	for i, w := range list {
		v := View{Worker: w, ZoneSlug: slugs[w.ZoneID]}
		v.Online = w.Status == StatusActive && w.LastSeen != nil && now.Sub(*w.LastSeen) <= OnlineWindow
		if exp, ok := details.Expiry[w.ID]; ok {
			v.CertExpiresAt = &exp
		}
		if job, ok := details.CurrentJob[w.ID]; ok {
			v.CurrentJobID = &job
		}
		out[i] = v
	}
	return out, nil
}
