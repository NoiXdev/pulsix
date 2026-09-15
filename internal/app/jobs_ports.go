// The gRPC port adapters: the job queue behind the three ports the worker
// endpoint consumes (Claimer, EventRecorder, ResultRecorder), plus the
// system auditor. The queue's own surface is richer; these adapters exist
// so internal/grpcsrv stays decoupled from it.
package app

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/proto"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
)

// jsonRaw wraps a wire payload string as raw JSON; the queue normalizes
// non-JSON payloads (bare stdout lines) into JSON strings at rest.
func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

// jobsClaimer adapts jobs.SQLQueue to grpcsrv.Claimer.
type jobsClaimer struct{ q *jobs.SQLQueue }

// Claim implements grpcsrv.Claimer. The payload moves with proto.Merge
// because a protobuf message must never be copied by value (vet copylocks);
// merging into a zero message is exactly a copy.
func (a jobsClaimer) Claim(ctx context.Context, zones []uuid.UUID, version string) (*grpcsrv.JobClaim, error) {
	c, err := a.q.Claim(ctx, zones, version)
	if err != nil || c == nil {
		return nil, err
	}
	out := &grpcsrv.JobClaim{Token: c.Token}
	proto.Merge(&out.Job, &c.Job)
	return out, nil
}

// jobsEventRecorder adapts jobs.SQLQueue to grpcsrv.EventRecorder: events
// arrive with the job id on them, and the queue verifies that a live claim
// binding covers that job before persisting anything.
type jobsEventRecorder struct{ q *jobs.SQLQueue }

// Append implements grpcsrv.EventRecorder.
func (a jobsEventRecorder) Append(ctx context.Context, ev grpcsrv.Event) error {
	jobID, err := uuid.Parse(ev.JobID)
	if err != nil {
		return err
	}
	return a.q.AppendEvent(ctx, jobID, jobs.Event{
		Seq: ev.Seq, Host: ev.Host, Task: ev.Task, State: ev.State,
		Payload: jsonRaw(ev.Payload), At: ev.At,
	})
}

// Cancelled implements grpcsrv.EventRecorder.
func (a jobsEventRecorder) Cancelled(ctx context.Context, jobID uuid.UUID) (bool, error) {
	return a.q.Cancelled(ctx, jobID)
}

// jobsResultRecorder adapts jobs.SQLQueue to grpcsrv.ResultRecorder.
type jobsResultRecorder struct{ q *jobs.SQLQueue }

// Finish implements grpcsrv.ResultRecorder. The queue's Finish is
// idempotent, so a worker retrying after a failed audit write is a no-op.
func (a jobsResultRecorder) Finish(ctx context.Context, res grpcsrv.Result) error {
	jobID, err := uuid.Parse(res.JobID)
	if err != nil {
		return err
	}
	return a.q.Finish(ctx, jobID, jobs.Status(res.Status), res.Stats, res.Error)
}

// repoRefSource adapts repos.Service to the jobs queue's RepoSource port:
// a repo playbook's claim resolves its repository through the same
// registry the API serves — url, branch, subpath and the git credential
// id, ids and addresses only.
type repoRefSource struct{ s *repos.Service }

// Get implements jobs.RepoSource.
func (a repoRefSource) Get(ctx context.Context, orgID, repoID uuid.UUID) (jobs.RepoRef, error) {
	r, err := a.s.Get(ctx, orgID, repoID)
	if err != nil {
		return jobs.RepoRef{}, err
	}
	return jobs.RepoRef{
		ID:           r.ID,
		URL:          r.URL,
		Branch:       r.Branch,
		Subpath:      r.Subpath,
		CredentialID: r.CredentialID,
	}, nil
}

// systemAuditor adapts audit.Recorder to grpcsrv.SystemAuditor: the gRPC
// endpoint's audit events are system events (no user behind them), with
// the org id parsed back out of its string form.
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
