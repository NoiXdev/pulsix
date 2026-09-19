package schedules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// Store persists schedules and their runs (org-scoped).
type Store interface {
	Create(ctx context.Context, s Schedule) (Schedule, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Schedule, error)
	List(ctx context.Context, orgID uuid.UUID, templateID *uuid.UUID) ([]Schedule, error)
	Update(ctx context.Context, s Schedule, revision int64) (Schedule, error)
	SetPaused(ctx context.Context, orgID, id uuid.UUID, paused bool, next *time.Time) (Schedule, error)
	PauseAll(ctx context.Context, orgID uuid.UUID) (int, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	RecordRun(ctx context.Context, r Run) error
	Runs(ctx context.Context, orgID, scheduleID uuid.UUID, limit int) ([]Run, error)
	LatestRuns(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]Run, error)
}

// TemplateChecker is the slice of templates.Service overrides need.
type TemplateChecker interface {
	ValidateOverrides(ctx context.Context, orgID, templateID uuid.UUID, in templates.Overrides, opt templates.OverrideOptions) (templates.Overrides, error)
	RevealOverrides(ctx context.Context, orgID, templateID uuid.UUID, stored templates.Overrides, aad []byte) (templates.Overrides, error)
}

// RunEnqueuer is the slice of jobs.Service a schedule fires through.
type RunEnqueuer interface {
	EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error)
	HasActiveRun(ctx context.Context, orgID, templateID uuid.UUID) (bool, error)
	// RunTargets lists the hosts a run of the template would target now
	// (the maintenance guard's question).
	RunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error)
}

// Auditor records the scheduled runs themselves (actor schedule:<id>).
type Auditor interface {
	Record(ctx context.Context, e audit.Event) error
}

// Service manages schedules and decides due occurrences.
type Service struct {
	store Store
	tmpl  TemplateChecker
	runs  RunEnqueuer
	audit Auditor
	now   func() time.Time
	rand  io.Reader
	log   *slog.Logger

	// maint answers the maintenance guard (SetMaintenance). Nil fails a
	// schedule whose guard needs it closed: recorded as failed, never run.
	maint MaintenanceChecker
}

// NewService wires the service; now and rand are injected for tests.
func NewService(store Store, tmpl TemplateChecker, runs RunEnqueuer, auditor Auditor, now func() time.Time, rand io.Reader, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, tmpl: tmpl, runs: runs, audit: auditor, now: now, rand: rand, log: log}
}

func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// Create validates and stores a new schedule; next_run_at is computed from now.
func (s *Service) Create(ctx context.Context, orgID, actor uuid.UUID, in Input) (Schedule, error) {
	id := s.newID()
	sch, err := s.build(ctx, orgID, id, in, nil)
	if err != nil {
		return Schedule{}, err
	}
	sch.CreatedBy = &actor
	return s.store.Create(ctx, sch)
}

// Update replaces a schedule when revision is still current. A paused
// schedule stays paused (next_run_at stays NULL).
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input) (Schedule, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Schedule{}, err
	}
	if cur.Revision != revision {
		return Schedule{}, ErrRevisionMismatch
	}
	sch, err := s.build(ctx, orgID, id, in, cur.Overrides.ExtraVars)
	if err != nil {
		return Schedule{}, err
	}
	sch.Paused, sch.CreatedBy, sch.LastRunAt = cur.Paused, cur.CreatedBy, cur.LastRunAt
	if cur.Paused {
		sch.NextRunAt = nil
	}
	return s.store.Update(ctx, sch, revision)
}

func (s *Service) build(ctx context.Context, orgID, id uuid.UUID, in Input, stored map[string]any) (Schedule, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return Schedule{}, err
	}
	now := s.now()
	tt, err := compileTimetable(in.Mode, in.Cron, in.Simple, in.Timezone, now)
	if err != nil {
		return Schedule{}, err
	}
	guards, err := normalizeGuards(in.Guards)
	if err != nil {
		return Schedule{}, err
	}
	ov, err := s.tmpl.ValidateOverrides(ctx, orgID, in.TemplateID, in.Overrides, templates.OverrideOptions{SealAAD: AAD(orgID, id), Stored: stored})
	if err != nil {
		return Schedule{}, templateErr(err)
	}
	next := tt.expr.Next(now, tt.loc)
	return Schedule{
		ID: id, OrgID: orgID, TemplateID: in.TemplateID, Name: name, Mode: in.Mode,
		Cron: tt.cron, Simple: tt.simple, Timezone: in.Timezone, Guards: guards,
		Overrides: ov, NextRunAt: &next,
	}, nil
}

// templateErr keeps schema problems (the API turns them into 422 details)
// and folds everything else the template side refuses into ErrInvalidInput.
func templateErr(err error) error {
	var ve *schema.ValidationError
	switch {
	case errors.As(err, &ve):
		return err
	case errors.Is(err, templates.ErrNotFound):
		return fmt.Errorf("%w: the job template does not exist", ErrInvalidInput)
	case errors.Is(err, templates.ErrInvalidInput):
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return err
}

func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Schedule, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns the org's schedules (optionally one template's) and the
// latest recorded run per schedule for the list's "Last" column.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, templateID *uuid.UUID) ([]Schedule, map[uuid.UUID]Run, error) {
	list, err := s.store.List(ctx, orgID, templateID)
	if err != nil {
		return nil, nil, err
	}
	latest, err := s.store.LatestRuns(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	return list, latest, nil
}

func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// Pause stops the schedule; next_run_at is cleared.
func (s *Service) Pause(ctx context.Context, orgID, id uuid.UUID) (Schedule, error) {
	return s.store.SetPaused(ctx, orgID, id, true, nil)
}

// Resume restarts the schedule from now: a long pause never replays.
func (s *Service) Resume(ctx context.Context, orgID, id uuid.UUID) (Schedule, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Schedule{}, err
	}
	tt, err := compileTimetable(ModeCron, cur.Cron, nil, cur.Timezone, s.now())
	if err != nil {
		return Schedule{}, err
	}
	next := tt.expr.Next(s.now(), tt.loc)
	return s.store.SetPaused(ctx, orgID, id, false, &next)
}

// PauseAll pauses every active schedule of the org and returns how many.
func (s *Service) PauseAll(ctx context.Context, orgID uuid.UUID) (int, error) {
	return s.store.PauseAll(ctx, orgID)
}

// RunNow fires the schedule once, outside its timetable, as the operator.
func (s *Service) RunNow(ctx context.Context, orgID, id, actor uuid.UUID) ([]jobs.Job, error) {
	sch, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	run := Run{ID: s.newID(), OrgID: orgID, ScheduleID: id, DueAt: now, DecidedAt: now}
	created, err := s.enqueue(ctx, sch, &actor, nil)
	if err != nil {
		run.Outcome, run.Reason = OutcomeFailed, failureReason(err)
	} else {
		run.Outcome, run.Reason, run.JobGroupID = OutcomeTriggered, "run now", &created[0].RunGroupID
	}
	if rerr := s.store.RecordRun(ctx, run); rerr != nil {
		return nil, fmt.Errorf("record run: %w", rerr)
	}
	return created, err
}

// Runs lists the newest recorded runs of one schedule.
func (s *Service) Runs(ctx context.Context, orgID, id uuid.UUID, limit int) ([]Run, error) {
	if _, err := s.store.Get(ctx, orgID, id); err != nil {
		return nil, err
	}
	return s.store.Runs(ctx, orgID, id, limit)
}

// Preview compiles an editor's input and lists the next five occurrences.
func (s *Service) Preview(in PreviewInput) (Preview, error) {
	now := s.now()
	tt, err := compileTimetable(in.Mode, in.Cron, in.Simple, in.Timezone, now)
	if err != nil {
		return Preview{}, err
	}
	return Preview{Cron: tt.cron, Description: describe(tt), Next: tt.expr.NextN(now, tt.loc, 5)}, nil
}

func describe(tt timetable) string {
	if tt.simple != nil {
		return tt.simple.Describe()
	}
	return cronexpr.Describe(tt.cron)
}

// enqueue reveals the schedule's overrides and enqueues one run with
// trigger schedule / trigger_ref schedule id, leaving out the exclude
// hosts, inside the schedule's org scope (the ticker's context carries
// none).
func (s *Service) enqueue(ctx context.Context, sch Schedule, actor *uuid.UUID, exclude []uuid.UUID) ([]jobs.Job, error) {
	ctx = db.WithOrgScope(ctx, sch.OrgID)
	var ov *jobs.RunOverrides
	if !sch.Overrides.Empty() {
		revealed, err := s.tmpl.RevealOverrides(ctx, sch.OrgID, sch.TemplateID, sch.Overrides, AAD(sch.OrgID, sch.ID))
		if err != nil {
			return nil, fmt.Errorf("reveal overrides: %w", err)
		}
		ov = &jobs.RunOverrides{Limit: revealed.Limit, ExtraVars: revealed.ExtraVars}
	}
	ref := sch.ID
	return s.runs.EnqueueRunWith(ctx, sch.OrgID, sch.TemplateID, jobs.RunRequest{
		Trigger: jobs.TriggerSchedule, TriggerRef: &ref, Actor: actor, Overrides: ov, ExcludeHosts: exclude,
	})
}

// failureReason is the generic, value-free reason a failed run records;
// the detail goes to the manager log.
func failureReason(err error) string {
	var ve *schema.ValidationError
	switch {
	case errors.Is(err, jobs.ErrEmptyScope):
		return "the template's scope resolves to no host"
	case errors.Is(err, jobs.ErrHostKeyChanged):
		return "every targeted host has a changed host key"
	case errors.Is(err, jobs.ErrNotFound), errors.Is(err, templates.ErrNotFound):
		return "the job template no longer exists"
	case errors.As(err, &ve), errors.Is(err, templates.ErrInvalidInput):
		return "the schedule's overrides no longer match the template"
	}
	return "could not enqueue the run (see the manager log)"
}
