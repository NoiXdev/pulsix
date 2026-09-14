// Temporary gRPC port implementations. The real job queue (Claimer,
// EventRecorder, ResultRecorder) is Task 5's `internal/jobs` package: until
// that lands, PULSIX_GRPC_LISTEN serves an endpoint that authenticates
// workers correctly but always reports an empty queue and accepts no-op
// events — enough to exercise the mTLS path end to end, documented here so
// nobody mistakes it for a complete queue.
package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/grpcsrv"
)

// ErrNotWired is what every stub's error-capable call reports: the port is
// served, but nothing behind it yet.
var ErrNotWired = errors.New("gRPC port not wired until the job queue lands (Task 5)")

// stubClaimer claims nothing: the queue is empty by definition.
type stubClaimer struct{}

// Claim implements grpcsrv.Claimer.
func (stubClaimer) Claim(context.Context, []uuid.UUID, string) (*grpcsrv.JobClaim, error) {
	return nil, nil
}

// stubEventRecorder swallows events: there is no job to record them on.
type stubEventRecorder struct{}

// Append implements grpcsrv.EventRecorder.
func (stubEventRecorder) Append(context.Context, grpcsrv.Event) error { return ErrNotWired }

// Cancelled implements grpcsrv.EventRecorder: never cancelling, because
// there is no job to cancel.
func (stubEventRecorder) Cancelled(context.Context, uuid.UUID) (bool, error) { return false, nil }

// stubResultRecorder accepts results without persisting them.
type stubResultRecorder struct{}

// Finish implements grpcsrv.ResultRecorder.
func (stubResultRecorder) Finish(context.Context, grpcsrv.Result) error { return ErrNotWired }

// systemAuditor adapts audit.Recorder to grpcsrv.SystemAuditor: the gRPC
// endpoint's audit events are system events (no user behind them), with the
// org id parsed back out of its string form.
type systemAuditor struct{ rec *audit.Recorder }

// Record implements grpcsrv.SystemAuditor.
func (a systemAuditor) Record(ctx context.Context, ev grpcsrv.SystemEvent) error {
	se := audit.SystemEvent{
		Operation:  ev.Operation,
		ObjectType: ev.ObjectType,
		ObjectID:   ev.ObjectID,
		Summary:    ev.Summary,
	}
	if ev.OrgID != "" {
		id, err := uuid.Parse(ev.OrgID)
		if err != nil {
			return err
		}
		se.OrgID = &id
	}
	return a.rec.RecordSystem(ctx, se)
}
