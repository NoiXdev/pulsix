// ClaimJob: parse the request, refresh the worker's heartbeat, hand the
// zones to the Claimer port, bind the returned job to its stream token and
// audit the claim as a system event.
package grpcsrv

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// ClaimJob implements workerpb.WorkerServiceServer.ClaimJob. An empty
// response (no job) means the queues behind the presented zones are empty.
func (s *Server) ClaimJob(ctx context.Context, req *workerpb.ClaimJobRequest) (*workerpb.ClaimJobResponse, error) {
	_, worker, err := authenticated(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := parseZoneIDs(req.GetZoneIds())
	if err != nil {
		return nil, err
	}

	// The claim doubles as the worker's heartbeat: Touch records last_seen
	// and the self-reported build version.
	if err := s.deps.Workers.Touch(ctx, worker.CertSerial, req.GetVersion()); err != nil {
		return nil, status.Errorf(codes.Internal, "touch worker: %v", err)
	}

	claim, err := s.deps.Claims.Claim(ctx, zones, req.GetVersion())
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

// parseZoneIDs converts the request's zone ids; an unparseable one is the
// caller's error, not the queue's.
func parseZoneIDs(raw []string) ([]uuid.UUID, error) {
	zones := make([]uuid.UUID, 0, len(raw))
	for _, r := range raw {
		id, err := uuid.Parse(r)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "zone id %q: %v", r, err)
		}
		zones = append(zones, id)
	}
	return zones, nil
}
