package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope — the same seam as
// hosts.SQLStore.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func (s *SQLStore) CreateJob(ctx context.Context, j Job, payload json.RawMessage) error {
	hostIDs := j.HostIDs
	if hostIDs == nil {
		// host_ids is NOT NULL DEFAULT '{}' — an explicit empty array, not
		// NULL, keeps the column's contract.
		hostIDs = []uuid.UUID{}
	}
	_, err := s.q.CreateJob(ctx, gen.CreateJobParams{
		ID: j.ID, OrgID: j.OrgID, ZoneID: j.ZoneID, Kind: string(j.Kind),
		TemplateID: db.NullUUID(j.TemplateID), HostIds: hostIDs, Status: string(j.Status),
		RunGroupID: j.RunGroupID, Trigger: j.Trigger, ActorID: db.NullUUID(j.ActorID),
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (s *SQLStore) GetJob(ctx context.Context, orgID, id uuid.UUID) (Job, error) {
	row, err := s.q.GetJob(ctx, gen.GetJobParams{OrgID: orgID, ID: id})
	if err != nil {
		return Job{}, mapErr(err)
	}
	return toJob(row), nil
}

func (s *SQLStore) ListJobs(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]Job, string, error) {
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListJobs(ctx, gen.ListJobsParams{
		OrgID:    orgID,
		ZoneID:   uuidToPg(f.ZoneID),
		Status:   textToPg(statusPtr(f.Status)),
		Kind:     textToPg(kindPtr(f.Kind)),
		CursorAt: timeToPg(cursor, cursorAt),
		CursorID: uuidToPg(cursorIDPtr(cursor, cursorID)),
		RowLimit: int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Service.List before it ever reaches here
	})
	if err != nil {
		return nil, "", fmt.Errorf("list jobs: %w", err)
	}
	out := make([]Job, len(rows))
	for i, r := range rows {
		out[i] = toJob(r)
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = EncodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}
	return out, next, nil
}

func (s *SQLStore) CancelJob(ctx context.Context, orgID, id uuid.UUID) (Status, error) {
	status, err := s.q.CancelJob(ctx, gen.CancelJobParams{OrgID: orgID, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// After the caller's own GetJob this means the job left the
			// cancellable set in between (or was deleted): either way
			// there is nothing to cancel.
			return "", ErrNotRunning
		}
		return "", fmt.Errorf("cancel job: %w", err)
	}
	return Status(status), nil
}

func (s *SQLStore) ListJobEvents(ctx context.Context, orgID, jobID uuid.UUID, after int64) ([]Event, error) {
	rows, err := s.q.ListJobEvents(ctx, gen.ListJobEventsParams{OrgID: orgID, JobID: jobID, Seq: after})
	if err != nil {
		return nil, fmt.Errorf("list job events: %w", err)
	}
	out := make([]Event, len(rows))
	for i, r := range rows {
		out[i] = Event{Seq: uint64(r.Seq), Host: r.Host, Task: r.Task, State: r.State, Payload: json.RawMessage(r.Payload), At: r.At}
	}
	return out, nil
}

func (s *SQLStore) RecordSyncResult(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, errText string) error {
	n, err := s.q.UpdateRepoSyncState(ctx, gen.UpdateRepoSyncStateParams{
		OrgID: orgID, ID: repoID,
		LastSync:      pgtype.Timestamptz{Time: at, Valid: true},
		LastSyncError: syncErrorPtr(errText),
	})
	if err != nil {
		return fmt.Errorf("record sync result: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// syncErrorPtr maps an empty failure text (a succeeded sync) onto NULL, so
// a success clears the previous error rather than storing "".
func syncErrorPtr(errText string) *string {
	if errText == "" {
		return nil
	}
	return &errText
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func toJob(r gen.Job) Job {
	var stats map[string]any
	_ = json.Unmarshal(r.Stats, &stats)
	return Job{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Kind: Kind(r.Kind),
		TemplateID: db.UUIDPtr(r.TemplateID), HostIDs: r.HostIds, Status: Status(r.Status),
		WorkerID:  db.UUIDPtr(r.WorkerID),
		ClaimedAt: db.TimePtr(r.ClaimedAt), StartedAt: db.TimePtr(r.StartedAt), FinishedAt: db.TimePtr(r.FinishedAt),
		Stats: stats, Error: r.Error, RunGroupID: r.RunGroupID, Trigger: r.Trigger,
		ActorID: db.UUIDPtr(r.ActorID), CreatedAt: r.CreatedAt, Payload: json.RawMessage(r.Payload),
	}
}

// pgtype helpers for the optional ListJobs filters and cursor, mirroring
// hosts.SQLStore's cursorToPg shape.
func uuidToPg(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func textToPg(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

func timeToPg(cursor string, at time.Time) pgtype.Timestamptz {
	if cursor == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: at, Valid: true}
}

func cursorIDPtr(cursor string, id uuid.UUID) *uuid.UUID {
	if cursor == "" {
		return nil
	}
	return &id
}

func statusPtr(s *Status) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}

func kindPtr(k *Kind) *string {
	if k == nil {
		return nil
	}
	v := string(*k)
	return &v
}
