// ReportResult: the terminal unary call. The status must be one of the
// known set, the job id must parse, and the stats JSON (when present) must
// decode — all caller errors; everything else goes to the ResultRecorder
// port and one system audit event.
package grpcsrv

import (
	"context"
	"encoding/json"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// resultStatuses is the closed set ReportResult accepts.
var resultStatuses = map[string]bool{
	"succeeded": true,
	"failed":    true,
	"cancelled": true,
	"error":     true,
}

// ReportResult implements workerpb.WorkerServiceServer.ReportResult.
func (s *Server) ReportResult(ctx context.Context, req *workerpb.ReportResultRequest) (*workerpb.ReportResultResponse, error) {
	_, _, err := authenticated(ctx)
	if err != nil {
		return nil, err
	}
	jobID, err := uuid.Parse(req.GetJobId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "job id %q: %v", req.GetJobId(), err)
	}
	if !resultStatuses[req.GetStatus()] {
		return nil, status.Errorf(codes.InvalidArgument, "status %q is not one of succeeded, failed, cancelled, error", req.GetStatus())
	}
	stats, err := parseStats(req.GetStats())
	if err != nil {
		return nil, err
	}
	res := Result{JobID: jobID.String(), Status: req.GetStatus(), Stats: stats, Error: req.GetError()}
	if err := s.deps.Results.Finish(ctx, res); err != nil {
		return nil, status.Errorf(codes.Internal, "record job result: %v", err)
	}
	// The org is not carried on the wire: ResultRecorder.Finish resolves
	// the job org-unscoped, and the audit event names the job id instead.
	if err := s.deps.Audit.Record(ctx, SystemEvent{
		Operation:  "job.result",
		ObjectType: "job",
		ObjectID:   jobID.String(),
		Summary:    "job finished with status " + res.Status,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "audit job result: %v", err)
	}
	// NOTE: ids and status only — never the payload, never the error text
	// verbatim from a log-capture perspective (see server_test.go).
	slog.InfoContext(ctx, "job result recorded", "job_id", jobID, "status", res.Status)
	return &workerpb.ReportResultResponse{}, nil
}

// parseStats decodes the worker's stats JSON into the map Result carries.
// An empty string is "no stats"; anything present must be a JSON object.
func parseStats(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(raw), &stats); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "stats: %v", err)
	}
	if stats == nil { // the literal JSON null
		return nil, status.Error(codes.InvalidArgument, "stats must be a JSON object")
	}
	return stats, nil
}
