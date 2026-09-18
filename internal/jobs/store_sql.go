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
// hosts.SQLStore. The db handle is kept for the one multi-statement
// operation the queue needs (CreateJobGroup's single transaction).
type SQLStore struct {
	q *gen.Queries
	d *db.DB
}

// NewSQLStore builds the store on d itself, never on its raw pool.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

// CreateJobGroup inserts every job of one enqueue group inside a single
// transaction: all rows commit together or nothing does — a rotation
// enqueue that fails halfway must never leave an orphaned partial group
// behind (whose rotate_key jobs could run and finalize on their own).
// payloads maps job id to the already-sealed stored form (nil for jobs
// without one). The org scope is bound explicitly, so the inserts pass the
// org_isolation policy the same way every org-scoped write does.
func (s *SQLStore) CreateJobGroup(ctx context.Context, orgID uuid.UUID, jobs []Job, payloads map[uuid.UUID]json.RawMessage) error {
	return s.d.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		return insertJobs(ctx, gen.New(tx), jobs, payloads)
	})
}

// CreateRotationGroup is CreateJobGroup for a credential's rotation group,
// exclusive per credential: under a per-credential advisory transaction
// lock it refuses with ErrRotationInProgress while any job of an earlier
// rotation group of the same credential is not terminal, and inserts the
// group otherwise — two concurrent Rotate calls cannot both pass the check.
func (s *SQLStore) CreateRotationGroup(ctx context.Context, orgID, credentialID uuid.UUID, jobs []Job, payloads map[uuid.UUID]json.RawMessage) error {
	return s.d.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, rotationLockKeyPrefix+credentialID.String()); err != nil {
			return fmt.Errorf("lock credential rotation: %w", err)
		}
		q := gen.New(tx)
		active, err := q.CountActiveRotationJobs(ctx, gen.CountActiveRotationJobsParams{
			OrgID: orgID, RotationCredentialID: pgtype.UUID{Bytes: credentialID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("check active rotation: %w", err)
		}
		if active > 0 {
			return ErrRotationInProgress
		}
		return insertJobs(ctx, q, jobs, payloads)
	})
}

// rotationLockKeyPrefix namespaces the per-credential rotation lock within
// hashtext's key space.
const rotationLockKeyPrefix = "credential-rotation:"

// insertJobs inserts jobs through q (a transaction's queries).
func insertJobs(ctx context.Context, q *gen.Queries, jobs []Job, payloads map[uuid.UUID]json.RawMessage) error {
	for _, j := range jobs {
		if _, err := q.CreateJob(ctx, createJobParams(j, payloads[j.ID])); err != nil {
			return fmt.Errorf("create job %s: %w", j.ID, err)
		}
	}
	return nil
}

// createJobParams maps a job and its stored payload onto the insert.
func createJobParams(j Job, payload json.RawMessage) gen.CreateJobParams {
	hostIDs := j.HostIDs
	if hostIDs == nil {
		// host_ids is NOT NULL DEFAULT '{}' — an explicit empty array, not
		// NULL, keeps the column's contract.
		hostIDs = []uuid.UUID{}
	}
	trigger := j.Trigger
	if trigger == "" {
		// Several existing integration tests insert jobs without a
		// trigger; the insert always names the column, so the DB default
		// never applied and the new jobs_trigger_check would refuse ''.
		trigger = TriggerManual
	}
	return gen.CreateJobParams{
		ID: j.ID, OrgID: j.OrgID, ZoneID: j.ZoneID, Kind: string(j.Kind),
		TemplateID: db.NullUUID(j.TemplateID), HostIds: hostIDs, Status: string(j.Status),
		RunGroupID: j.RunGroupID, Trigger: trigger, ActorID: db.NullUUID(j.ActorID),
		Payload:   payload,
		DependsOn: db.NullUUID(j.DependsOn), RotationCredentialID: db.NullUUID(j.RotationCredentialID),
		TriggerRef: db.NullUUID(j.TriggerRef),
	}
}

func (s *SQLStore) CreateJob(ctx context.Context, j Job, payload json.RawMessage) error {
	if _, err := s.q.CreateJob(ctx, createJobParams(j, payload)); err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (s *SQLStore) GetJob(ctx context.Context, orgID, id uuid.UUID) (Job, error) {
	row, err := s.q.GetJob(ctx, gen.GetJobParams{OrgID: orgID, ID: id})
	if err != nil {
		return Job{}, mapErr(err)
	}
	return toJobDetail(row), nil
}

func (s *SQLStore) ListJobs(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]Job, string, error) {
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListJobs(ctx, gen.ListJobsParams{
		OrgID:      orgID,
		ZoneID:     uuidToPg(f.ZoneID),
		Status:     textToPg(statusPtr(f.Status)),
		Kind:       textToPg(kindPtr(f.Kind)),
		TemplateID: uuidToPg(f.TemplateID),
		RunGroupID: uuidToPg(f.RunGroupID),
		CursorAt:   timeToPg(cursor, cursorAt),
		CursorID:   uuidToPg(cursorIDPtr(cursor, cursorID)),
		RowLimit:   int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Service.List before it ever reaches here
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
		out[i] = Event{Seq: uint64(r.Seq), Attempt: int(r.Attempt), Host: r.Host, Task: r.Task, State: r.State, Payload: json.RawMessage(r.Payload), At: r.At}
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

func (s *SQLStore) HasActiveTemplateJob(ctx context.Context, orgID, templateID uuid.UUID) (bool, error) {
	ok, err := s.q.HasActiveTemplateJob(ctx, gen.HasActiveTemplateJobParams{OrgID: orgID, TemplateID: pgtype.UUID{Bytes: templateID, Valid: true}})
	if err != nil {
		return false, fmt.Errorf("check active template job: %w", err)
	}
	return ok, nil
}

func (s *SQLStore) HasActiveRepoSync(ctx context.Context, orgID, repoID uuid.UUID) (bool, error) {
	ok, err := s.q.HasActiveRepoSync(ctx, gen.HasActiveRepoSyncParams{OrgID: orgID, RepoID: repoID.String()})
	if err != nil {
		return false, fmt.Errorf("check active repo sync: %w", err)
	}
	return ok, nil
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
		Attempts: int(r.Attempts), DependsOn: db.UUIDPtr(r.DependsOn), RotationCredentialID: db.UUIDPtr(r.RotationCredentialID),
		WorkerID:  db.UUIDPtr(r.WorkerID),
		ClaimedAt: db.TimePtr(r.ClaimedAt), StartedAt: db.TimePtr(r.StartedAt), FinishedAt: db.TimePtr(r.FinishedAt),
		Stats: stats, Error: r.Error, RunGroupID: r.RunGroupID, Trigger: r.Trigger,
		TriggerRef: db.UUIDPtr(r.TriggerRef),
		ActorID:    db.UUIDPtr(r.ActorID), CreatedAt: r.CreatedAt, Payload: json.RawMessage(r.Payload),
	}
}

// toJobDetail is toJob for the GetJob row, whose three display joins add
// the actor's, the worker's and the template's names. Empty when the
// underlying id was NULL or the joined row is gone (a dangling
// template_id reads as no template name; the API shows "(deleted)").
func toJobDetail(r gen.GetJobRow) Job {
	j := toJob(gen.Job{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Kind: r.Kind, TemplateID: r.TemplateID,
		HostIds: r.HostIds, Status: r.Status, WorkerID: r.WorkerID, ClaimedAt: r.ClaimedAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Stats: r.Stats, Error: r.Error,
		RunGroupID: r.RunGroupID, Trigger: r.Trigger, TriggerRef: r.TriggerRef, ActorID: r.ActorID, Payload: r.Payload,
		CreatedAt: r.CreatedAt, Attempts: r.Attempts, DependsOn: r.DependsOn, RotationCredentialID: r.RotationCredentialID,
	})
	j.ActorName, j.WorkerName, j.TemplateName = deref(r.ActorName), deref(r.WorkerName), deref(r.TemplateName)
	return j
}

// deref renders a nullable text join column as the empty string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
