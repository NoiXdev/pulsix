package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore is the org-scoped schedule store.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on *db.DB so every statement runs in the
// request's org scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func (st *SQLStore) Create(ctx context.Context, s Schedule) (Schedule, error) {
	simple, guards, vars, err := encodeJSON(s)
	if err != nil {
		return Schedule{}, err
	}
	row, err := st.q.CreateSchedule(ctx, gen.CreateScheduleParams{
		ID: s.ID, OrgID: s.OrgID, TemplateID: s.TemplateID, Name: s.Name, Mode: string(s.Mode), Cron: s.Cron,
		Simple: simple, Timezone: s.Timezone, Guards: guards, LimitOverride: s.Overrides.Limit, ExtraVars: vars,
		NextRunAt: db.NullTime(s.NextRunAt), CreatedBy: db.NullUUID(s.CreatedBy),
	})
	if db.IsUniqueViolation(err) {
		return Schedule{}, ErrNameTaken
	}
	if db.IsForeignKeyViolation(err) {
		return Schedule{}, fmt.Errorf("%w: the job template does not exist", ErrInvalidInput)
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("create schedule: %w", err)
	}
	return toSchedule(row)
}

func (st *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Schedule, error) {
	row, err := st.q.GetSchedule(ctx, gen.GetScheduleParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("get schedule: %w", err)
	}
	return toSchedule(row)
}

func (st *SQLStore) List(ctx context.Context, orgID uuid.UUID, templateID *uuid.UUID) ([]Schedule, error) {
	rows, err := st.q.ListSchedules(ctx, gen.ListSchedulesParams{OrgID: orgID, TemplateID: db.NullUUID(templateID)})
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	out := make([]Schedule, 0, len(rows))
	for _, r := range rows {
		s, err := toSchedule(r)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (st *SQLStore) Update(ctx context.Context, s Schedule, revision int64) (Schedule, error) {
	simple, guards, vars, err := encodeJSON(s)
	if err != nil {
		return Schedule{}, err
	}
	row, err := st.q.UpdateSchedule(ctx, gen.UpdateScheduleParams{
		OrgID: s.OrgID, ID: s.ID, Revision: revision, TemplateID: s.TemplateID, Name: s.Name, Mode: string(s.Mode),
		Cron: s.Cron, Simple: simple, Timezone: s.Timezone, Guards: guards, LimitOverride: s.Overrides.Limit,
		ExtraVars: vars, NextRunAt: db.NullTime(s.NextRunAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.Get(ctx, s.OrgID, s.ID); gerr != nil {
			return Schedule{}, gerr
		}
		return Schedule{}, ErrRevisionMismatch
	}
	if db.IsUniqueViolation(err) {
		return Schedule{}, ErrNameTaken
	}
	if db.IsForeignKeyViolation(err) {
		return Schedule{}, fmt.Errorf("%w: the job template does not exist", ErrInvalidInput)
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("update schedule: %w", err)
	}
	return toSchedule(row)
}

func (st *SQLStore) SetPaused(ctx context.Context, orgID, id uuid.UUID, paused bool, next *time.Time) (Schedule, error) {
	row, err := st.q.SetSchedulePaused(ctx, gen.SetSchedulePausedParams{OrgID: orgID, ID: id, Paused: paused, NextRunAt: db.NullTime(next)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("set schedule paused: %w", err)
	}
	return toSchedule(row)
}

func (st *SQLStore) PauseAll(ctx context.Context, orgID uuid.UUID) (int, error) {
	n, err := st.q.PauseAllSchedules(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("pause all schedules: %w", err)
	}
	return int(n), nil
}

func (st *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := st.q.DeleteSchedule(ctx, gen.DeleteScheduleParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (st *SQLStore) RecordRun(ctx context.Context, r Run) error {
	return insertRun(ctx, st.q, r)
}

func (st *SQLStore) Runs(ctx context.Context, orgID, scheduleID uuid.UUID, limit int) ([]Run, error) {
	rows, err := st.q.ListScheduleRuns(ctx, gen.ListScheduleRunsParams{OrgID: orgID, ScheduleID: scheduleID, Lim: int32(limit)}) //nolint:gosec // limit is clamped by the API (≤ 100)
	if err != nil {
		return nil, fmt.Errorf("list schedule runs: %w", err)
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = toRun(r)
	}
	return out, nil
}

func (st *SQLStore) LatestRuns(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]Run, error) {
	rows, err := st.q.LatestScheduleRuns(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("latest schedule runs: %w", err)
	}
	out := make(map[uuid.UUID]Run, len(rows))
	for _, r := range rows {
		out[r.ScheduleID] = toRun(r)
	}
	return out, nil
}

func (st *SQLStore) RunsBetween(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]Run, error) {
	rows, err := st.q.ListScheduleRunsBetween(ctx, gen.ListScheduleRunsBetweenParams{OrgID: orgID, FromAt: from, ToAt: to})
	if err != nil {
		return nil, fmt.Errorf("list schedule runs between: %w", err)
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = toRun(r)
	}
	return out, nil
}

// insertRun is shared by the org-scoped store and the ticker's bypass tx.
func insertRun(ctx context.Context, q *gen.Queries, r Run) error {
	if err := q.InsertScheduleRun(ctx, gen.InsertScheduleRunParams{
		ID: r.ID, OrgID: r.OrgID, ScheduleID: r.ScheduleID, DueAt: r.DueAt, DecidedAt: r.DecidedAt,
		Outcome: string(r.Outcome), Reason: r.Reason, JobGroupID: db.NullUUID(r.JobGroupID),
	}); err != nil {
		return fmt.Errorf("record schedule run: %w", err)
	}
	return nil
}

func encodeJSON(s Schedule) (simple, guards, vars []byte, err error) {
	if s.Simple != nil {
		if simple, err = json.Marshal(s.Simple); err != nil {
			return nil, nil, nil, err
		}
	}
	if guards, err = json.Marshal(s.Guards); err != nil {
		return nil, nil, nil, err
	}
	if len(s.Overrides.ExtraVars) > 0 {
		if vars, err = json.Marshal(s.Overrides.ExtraVars); err != nil {
			return nil, nil, nil, err
		}
	}
	return simple, guards, vars, nil
}

func toSchedule(r gen.Schedule) (Schedule, error) {
	s := Schedule{
		ID: r.ID, OrgID: r.OrgID, TemplateID: r.TemplateID, Name: r.Name, Mode: Mode(r.Mode), Cron: r.Cron,
		Timezone: r.Timezone, Paused: r.Paused, NextRunAt: db.TimePtr(r.NextRunAt), LastRunAt: db.TimePtr(r.LastRunAt),
		CreatedBy: db.UUIDPtr(r.CreatedBy), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision,
	}
	s.Overrides.Limit = r.Limit
	if len(r.Simple) > 0 {
		s.Simple = &cronexpr.Simple{}
		if err := json.Unmarshal(r.Simple, s.Simple); err != nil {
			return Schedule{}, fmt.Errorf("decode schedule simple: %w", err)
		}
	}
	if err := json.Unmarshal(r.Guards, &s.Guards); err != nil {
		return Schedule{}, fmt.Errorf("decode schedule guards: %w", err)
	}
	if len(r.ExtraVars) > 0 {
		if err := json.Unmarshal(r.ExtraVars, &s.Overrides.ExtraVars); err != nil {
			return Schedule{}, fmt.Errorf("decode schedule extra vars: %w", err)
		}
	}
	return s, nil
}

func toRun(r gen.ScheduleRun) Run {
	return Run{
		ID: r.ID, OrgID: r.OrgID, ScheduleID: r.ScheduleID, DueAt: r.DueAt, DecidedAt: r.DecidedAt,
		Outcome: Outcome(r.Outcome), Reason: r.Reason, JobGroupID: db.UUIDPtr(r.JobGroupID),
	}
}
