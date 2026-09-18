// ClaimJob: authorize the requested zones against the authenticated
// worker, refresh the worker's heartbeat, hand the worker's claim scope
// (its own organization and zone) to the Claimer port, bind the returned job to its stream token and
// audit the claim as a system event.
package grpcsrv

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// ClaimJob implements workerpb.WorkerServiceServer.ClaimJob. An empty
// response (no job) means the queues behind the presented zones are empty.
func (s *Server) ClaimJob(ctx context.Context, req *workerpb.ClaimJobRequest) (*workerpb.ClaimJobResponse, error) {
	_, worker, err := authenticated(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := authorizedZones(worker, req.GetZoneIds())
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			slog.WarnContext(ctx, "worker claim refused: zone outside the worker's enrollment",
				"worker", worker.Name, "org", worker.OrgID, "zone", worker.ZoneID)
		}
		return nil, err
	}

	// The claim doubles as the worker's heartbeat: Touch records last_seen
	// and the self-reported build version.
	if err := s.deps.Workers.Touch(ctx, worker.ID, req.GetVersion()); err != nil {
		return nil, status.Errorf(codes.Internal, "touch worker: %v", err)
	}

	claim, err := s.deps.Claims.Claim(ctx, ClaimScope{WorkerID: worker.ID, OrgID: worker.OrgID, ZoneIDs: zones}, req.GetVersion())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "claim job: %v", err)
	}
	if claim == nil {
		return &workerpb.ClaimJobResponse{}, nil
	}

	jobID, err := uuid.Parse(claim.Job.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "claimed job has no usable id: %v", err)
	}
	s.registry.register(jobID, claim.Token)

	// The binding token ships inside the payload; every other field the
	// claimer filled already.
	job := &claim.Job
	job.WorkerToken = claim.Token

	// The audit write fails the claim: an unrecorded claim must not leave
	// the job shipped without a trace of who took it.
	if err := s.deps.Audit.Record(ctx, SystemEvent{
		Operation:  "job.claim",
		ObjectType: "job",
		ObjectID:   jobID.String(),
		OrgID:      job.GetOrgId(),
		Summary:    "job claimed by worker " + worker.Name,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "audit job claim: %v", err)
	}
	// NOTE: the log line names ids only — the payload carries credential
	// material that must never reach a log (see server_test.go's
	// log-capture test).
	slog.InfoContext(ctx, "worker claimed job", "job_id", jobID, "worker", worker.Name, "org", job.GetOrgId())
	return &workerpb.ClaimJobResponse{Job: job}, nil
}

// authorizedZones turns the request's zone ids into the zones the worker
// may claim for. A worker certificate is enrolled for exactly one zone of
// one organization (worker.ZoneID/OrgID, resolved from the registry by the
// auth interceptor), and that zone is the only one it may claim from:
//
//   - an empty list (the documented default — PULSIX_WORKER_ZONES unset)
//     resolves to the worker's own zone;
//   - a list naming only the worker's own zone (possibly repeated) is
//     accepted;
//   - any other zone — another organization's (zone ids are visible
//     through that organization's API) or a sibling zone of the worker's
//     own organization — refuses the whole claim with PermissionDenied.
//
// Refusing instead of silently dropping the foreign ids makes a
// misconfigured worker visible in its own logs rather than idling forever;
// nothing about the foreign zone's queue is revealed either way.
func authorizedZones(worker workers.Worker, raw []string) ([]uuid.UUID, error) {
	for _, r := range raw {
		id, err := uuid.Parse(r)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "zone id %q: %v", r, err)
		}
		if id != worker.ZoneID {
			return nil, status.Errorf(codes.PermissionDenied,
				"worker %q is not enrolled for zone %s (it may only claim for its own zone)", worker.Name, id)
		}
	}
	return []uuid.UUID{worker.ZoneID}, nil
}
