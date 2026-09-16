// Package grpcsrv is the manager's mTLS gRPC endpoint for zone workers
// (spec §14): it authenticates a connection by the client certificate the
// internal CA issued, looks the serial up in the worker registry, and then
// serves the three WorkerService calls — ClaimJob (atomically claim one
// queued job and bind the run to a one-time token), RunEvents (the event
// stream a run writes into, with the manager's cancel directive) and
// ReportResult (the terminal status write).
//
// The package owns transport only: business rules live in the domain
// services it reaches through the narrow ports declared here (Claimer,
// EventRecorder, ResultRecorder, SystemAuditor). Task 5's job queue
// implements the queue-side ports; internal/app wires everything.
package grpcsrv

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// JobClaim is one claimed job ready to ship to a worker. Task 5's queue
// fills the payload (ID, Kind, OrgID, ZoneID, ...); Token is a fresh
// binding token that pairs the job with the RunEvents stream.
type JobClaim struct {
	Job   workerpb.Job // Job payload; ID, Kind, OrgID, ZoneID, ... filled
	Token string       // random 128-bit hex binding token
}

// ClaimScope is what a claim may take from: the authenticated worker's own
// organization and the zones it is authorized for (today exactly its
// enrolled zone). The server builds it from the registry row the auth
// interceptor resolved, never from the request alone.
type ClaimScope struct {
	OrgID   uuid.UUID
	ZoneIDs []uuid.UUID
}

// Claimer picks one queued job for a worker. Org-unscoped at the database
// level by design (the claim runs outside pulsix.org_id), which is why the
// scope carries the worker's organization explicitly: an implementation
// must only ever return a job whose org and zone are inside scope.
type Claimer interface {
	// Claim picks one queued job for any of the scope's zones inside the
	// scope's organization, marks it claimed and returns its payload plus a
	// fresh binding token. nil claim = queue empty.
	Claim(ctx context.Context, scope ClaimScope, version string) (*JobClaim, error)
}

// Event is one run event a worker streams in mid-run.
type Event struct {
	JobID   string
	Seq     uint64
	Host    string
	Task    string
	State   string
	Payload string
	At      time.Time
}

// EventRecorder persists run events and answers whether the job a stream
// is bound to has been asked to cancel.
type EventRecorder interface {
	Append(ctx context.Context, ev Event) error
	// Cancelled reports whether the job is in 'cancelling' state.
	Cancelled(ctx context.Context, jobID uuid.UUID) (bool, error)
}

// Result is the terminal status a worker reports for a finished job.
type Result struct {
	JobID  string
	Status string // succeeded | failed | cancelled | error
	Stats  map[string]any
	Error  string
}

// ResultRecorder writes the terminal status.
type ResultRecorder interface {
	Finish(ctx context.Context, res Result) error
}

// SystemEvent is one system-generated audit entry: no user is behind a
// claim or a result report, so there is no actor to name.
type SystemEvent struct {
	Operation  string
	ObjectType string
	ObjectID   string
	OrgID      string
	Summary    string
}

// SystemAuditor records system-generated audit events.
type SystemAuditor interface {
	Record(ctx context.Context, ev SystemEvent) error
}
