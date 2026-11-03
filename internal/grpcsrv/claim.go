// ClaimJob: authorize the requested zones against the authenticated
// worker, refresh the worker's heartbeat, hand the worker's claim scope
// (its own organization and zone) to the Claimer port, bind the returned job to its stream token and
// audit the claim as a system event. A claim too large to send fails its
// job instead (failOversizeClaim).
package grpcsrv

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

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
			s.log().WarnContext(ctx, "worker claim refused: zone outside the worker's enrollment",
				"worker", worker.Name, "org", worker.OrgID, "zone", worker.ZoneID)
		}
		return nil, err
	}

	// The claim doubles as the worker's heartbeat: Touch records last_seen
	// and the self-reported build version.
	if err := s.deps.Workers.Touch(ctx, worker.ID, req.GetVersion()); err != nil {
		return nil, status.Errorf(codes.Internal, "touch worker: %v", err)
	}

	claim, err := s.deps.Claims.Claim(ctx, ClaimScope{WorkerID: worker.ID, OrgID: worker.OrgID, ZoneIDs: zones}, req.GetVersion(), req.GetCancelStopsPlay())
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

	// The binding token ships inside the payload; every other field the
	// claimer filled already.
	job := &claim.Job
	job.WorkerToken = claim.Token
	resp := &workerpb.ClaimJobResponse{Job: job}
	if size := proto.Size(resp); size > workerpb.MaxManagerToWorkerBytes {
		return s.failOversizeClaim(ctx, worker, jobID, size)
	}
	s.registry.register(jobID, claim.Token)

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
	s.log().InfoContext(ctx, "worker claimed job", "job_id", jobID, "worker", worker.Name, "org", job.GetOrgId())
	return resp, nil
}

// failOversizeClaim ends a claimed job whose response would exceed
// MaxManagerToWorkerBytes (backlog 7.62). gRPC would refuse to send it
// with ResourceExhausted after the claim committed; the job would then sit
// running until the reaper requeued it, be claimed and refused again, and
// only fail at the attempt limit with a reason that does not name the
// size. Instead the job fails at once, as the claiming worker's own result
// (the worker-bound terminal transition wipes the sealed payload and runs
// the sinks), with an error naming both sizes. The worker gets an empty
// response — it did nothing wrong — and its next claim takes the next
// queued job. No stream binding is registered for the unsent job.
func (s *Server) failOversizeClaim(ctx context.Context, worker workers.Worker, jobID uuid.UUID, size int) (*workerpb.ClaimJobResponse, error) {
	reason := fmt.Sprintf("the job's claim is %d bytes, over the %d bytes a worker accepts; it was never sent to a worker "+
		"(the inventory snapshot with its host variables and facts is usually most of it — target fewer hosts)",
		size, workerpb.MaxManagerToWorkerBytes)
	if err := s.deps.Results.Finish(ctx, Result{
		JobID: jobID.String(), Status: "error", Error: reason,
		Worker: WorkerRef{ID: worker.ID, OrgID: worker.OrgID, ZoneID: worker.ZoneID},
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "fail oversize job claim: %v", err)
	}
	if err := s.deps.Audit.Record(ctx, SystemEvent{
		Operation:  "job.result",
		ObjectType: "job",
		ObjectID:   jobID.String(),
		OrgID:      worker.OrgID.String(),
		Summary:    fmt.Sprintf("job failed: its claim of %d bytes exceeds the worker limit of %d bytes", size, workerpb.MaxManagerToWorkerBytes),
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "audit oversize job claim: %v", err)
	}
	s.log().WarnContext(ctx, "claimed job failed: its claim exceeds the worker message limit",
		"job_id", jobID, "worker", worker.Name, "org", worker.OrgID, "bytes", size, "limit", workerpb.MaxManagerToWorkerBytes)
	return &workerpb.ClaimJobResponse{}, nil
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
