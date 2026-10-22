package schedules_test

import (
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

type world struct {
	svc        *schedules.Service
	store      *fakeStore
	tmpl       *fakeTemplates
	runs       *fakeRuns
	audit      *fakeAudit
	org        uuid.UUID
	actor      uuid.UUID
	templateID uuid.UUID
	now        time.Time
	berlin     *time.Location
}

func newWorld(t *testing.T) *world {
	t.Helper()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		store: newFakeStore(), tmpl: &fakeTemplates{}, runs: &fakeRuns{}, audit: &fakeAudit{},
		org: uuid.Must(uuid.NewV7()), actor: uuid.Must(uuid.NewV7()), templateID: uuid.Must(uuid.NewV7()),
		now: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC), berlin: berlin,
	}
	w.svc = schedules.NewService(w.store, w.tmpl, w.runs, w.audit, func() time.Time { return w.now }, rand.Reader,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return w
}

func TestCreateSealsOverridesWithTheScheduleAAD(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	limit := "web*"
	in.Overrides = templates.Overrides{Limit: &limit, ExtraVars: map[string]any{"token": "s3cret"}}
	got, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if string(w.tmpl.lastOpt.SealAAD) != string(schedules.AAD(w.org, got.ID)) {
		t.Fatalf("seal aad = %q", w.tmpl.lastOpt.SealAAD)
	}
	if string(schedules.AAD(w.org, got.ID)) != "schedule:"+w.org.String()+":"+got.ID.String() {
		t.Fatalf("AAD format = %q", schedules.AAD(w.org, got.ID))
	}
	if *got.Overrides.Limit != "web*" || got.Overrides.ExtraVars["token"].(map[string]any)["v"] != "s3cret" {
		t.Fatalf("stored overrides = %+v", got.Overrides)
	}
}

func TestCreatePassesTemplateValidationErrorsThrough(t *testing.T) {
	w := newWorld(t)
	w.tmpl.validErr = templates.ErrNotFound
	if _, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID)); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("unknown template: %v", err)
	}
}

func TestUpdateRequiresTheCurrentRevision(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	in := validInput(w.templateID)
	in.Cron = "*/30 * * * *"
	if _, err := w.svc.Update(ctx, w.org, s.ID, s.Revision+1, in); !errors.Is(err, schedules.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	up, err := w.svc.Update(ctx, w.org, s.ID, s.Revision, in)
	if err != nil {
		t.Fatal(err)
	}
	if up.Cron != "*/30 * * * *" || !up.NextRunAt.Equal(time.Date(2026, 9, 18, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("updated = %+v", up)
	}
	// Keep markers resolve against the stored overrides.
	if w.tmpl.lastOpt.SealAAD == nil {
		t.Fatal("update must seal with the schedule AAD")
	}
}

func TestPauseAndResume(t *testing.T) {
	w := newWorld(t)
	s, _ := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	p, err := w.svc.Pause(ctx, w.org, s.ID)
	if err != nil || !p.Paused || p.NextRunAt != nil {
		t.Fatalf("paused = %+v, %v", p, err)
	}
	// A week later, resume computes from now and never replays the week.
	w.now = w.now.Add(7 * 24 * time.Hour)
	r, err := w.svc.Resume(ctx, w.org, s.ID)
	if err != nil || r.Paused || r.NextRunAt == nil || !r.NextRunAt.After(w.now) {
		t.Fatalf("resumed = %+v, %v", r, err)
	}
	if n, err := w.svc.PauseAll(ctx, w.org); err != nil || n != 1 {
		t.Fatalf("PauseAll = %d, %v", n, err)
	}
}

// TestResumeOnAnActiveScheduleIsANoOp is backlog 2.61: Resume used to
// recompute next_run_at from now unconditionally, so calling it on a
// schedule that was never paused — an idempotent retry of the same API
// call resending it is the realistic way this happens — could jump an
// already-due occurrence forward from now, silently skipping it. An active
// schedule's next_run_at must come back exactly as it was.
func TestResumeOnAnActiveScheduleIsANoOp(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	want := *s.NextRunAt
	w.now = w.now.Add(time.Hour) // time passes; a due occurrence is now overdue
	r, err := w.svc.Resume(ctx, w.org, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Paused || r.NextRunAt == nil || !r.NextRunAt.Equal(want) {
		t.Fatalf("resumed an active schedule = %+v, want next_run_at unchanged at %v", r, want)
	}
}

func TestRunNowEnqueuesWithRevealedOverridesAndRecordsTheRun(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	in.Overrides = templates.Overrides{ExtraVars: map[string]any{"token": "s3cret"}}
	s, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	created, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor)
	if err != nil || len(created) != 1 {
		t.Fatalf("RunNow = %v, %v", created, err)
	}
	req := w.runs.requests[0]
	if req.Trigger != jobs.TriggerSchedule || *req.TriggerRef != s.ID || *req.Actor != w.actor {
		t.Fatalf("request = %+v", req)
	}
	if req.Overrides == nil || req.Overrides.ExtraVars["token"] != "s3cret" {
		t.Fatalf("overrides = %+v", req.Overrides)
	}
	runs, _ := w.svc.Runs(ctx, w.org, s.ID, 10)
	if len(runs) != 1 || runs[0].Outcome != schedules.OutcomeTriggered || runs[0].Reason != "run now" || *runs[0].JobGroupID != created[0].RunGroupID {
		t.Fatalf("runs = %+v", runs)
	}
	if len(w.audit.events) != 0 {
		t.Fatal("run now is audited by the HTTP route, not the service")
	}
}

func TestRunNowRecordsAFailure(t *testing.T) {
	w := newWorld(t)
	s, _ := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	w.runs.err = jobs.ErrEmptyScope
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); !errors.Is(err, jobs.ErrEmptyScope) {
		t.Fatalf("RunNow = %v", err)
	}
	runs, _ := w.svc.Runs(ctx, w.org, s.ID, 10)
	if len(runs) != 1 || runs[0].Outcome != schedules.OutcomeFailed || runs[0].Reason != "the template's scope resolves to no host" {
		t.Fatalf("runs = %+v", runs)
	}
}

// TestRunNowReportsSuccessEvenWhenRecordRunFails is backlog 2.61: the
// enqueue already happened — the operator's run now REALLY started — by the
// time RecordRun's own failure could occur, so returning an error here used
// to tell the operator the call failed when it had not, inviting a retry
// that fires the template a second time (RunNow carries no idempotency key;
// unlike the ticker's retried decision, a second RunNow call is a genuine
// second run). The fix logs the bookkeeping failure and still reports the
// real outcome: the jobs RunNow actually created.
func TestRunNowReportsSuccessEvenWhenRecordRunFails(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	w.store.recordRunErr = errors.New("db down")
	created, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor)
	if err != nil {
		t.Fatalf("RunNow = %v, want nil: the enqueue succeeded, only its bookkeeping row failed", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1", len(created))
	}
	if len(w.runs.requests) != 1 {
		t.Fatalf("enqueue attempts = %d, want 1 — an operator who sees success has no reason to retry, and must not be invited to", len(w.runs.requests))
	}
}

// TestRunNowReturnsTheEnqueueFailureEvenWhenRecordRunAlsoFails: when the
// enqueue itself fails, that is the error the operator needs to see — a
// bookkeeping failure on top of it must not replace it with a generic
// "record run" error that hides what actually happened.
func TestRunNowReturnsTheEnqueueFailureEvenWhenRecordRunAlsoFails(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	w.runs.err = jobs.ErrEmptyScope
	w.store.recordRunErr = errors.New("db down")
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); !errors.Is(err, jobs.ErrEmptyScope) {
		t.Fatalf("RunNow = %v, want the original enqueue failure (ErrEmptyScope), not a bookkeeping one", err)
	}
}

// TestRunNowTrimsRunHistory is backlog 2.61: RunNow used to insert a
// schedule_runs row without ever applying the ticker's own retention trim
// (TrimScheduleRuns / keepRuns), so a schedule "run now"d repeatedly, never
// through the ticker, never trimmed at all.
func TestRunNowTrimsRunHistory(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); err != nil {
		t.Fatal(err)
	}
	if w.store.trimCalls != 1 {
		t.Fatalf("TrimRuns calls = %d, want 1", w.store.trimCalls)
	}
}

// TestRunsClampsAnOutOfRangeLimit is backlog 2.61: Runs used to pass its
// limit straight to the store, relying entirely on the API's own clamp
// (schedule_handlers.go's clampListLimit) — Runs is itself an exported
// entry point, reachable without going through that handler.
func TestRunsClampsAnOutOfRangeLimit(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		at := w.now.Add(time.Duration(i) * time.Minute)
		if err := w.store.RecordRun(ctx, schedules.Run{
			ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ScheduleID: s.ID, DueAt: at, DecidedAt: at, Outcome: schedules.OutcomeTriggered,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if runs, err := w.svc.Runs(ctx, w.org, s.ID, 0); err != nil || len(runs) != 50 {
		t.Fatalf("Runs(limit=0) = %d runs, %v, want the default of 50", len(runs), err)
	}
	if runs, err := w.svc.Runs(ctx, w.org, s.ID, 10000); err != nil || len(runs) != 100 {
		t.Fatalf("Runs(limit=10000) = %d runs, %v, want clamped to 100", len(runs), err)
	}
	if runs, err := w.svc.Runs(ctx, w.org, s.ID, -5); err != nil || len(runs) != 50 {
		t.Fatalf("Runs(limit=-5) = %d runs, %v, want the default of 50", len(runs), err)
	}
}

// TestRunNowRecordsAnUnresolvedSharedTemplateDistinctly is
// TestRunNowRecordsAFailure's own test for jobs.ErrTemplateNotReady (v0.2f
// Task 6 review): a tick against a shared template projection whose
// playbook has not synced yet must record THIS failureReason case, not
// fall through to the value-free default ("could not enqueue the run (see
// the manager log)") that every ordinary enqueue failure share.Service
// does not otherwise recognize would have produced.
func TestRunNowRecordsAnUnresolvedSharedTemplateDistinctly(t *testing.T) {
	w := newWorld(t)
	s, _ := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	w.runs.err = jobs.ErrTemplateNotReady
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); !errors.Is(err, jobs.ErrTemplateNotReady) {
		t.Fatalf("RunNow = %v", err)
	}
	runs, _ := w.svc.Runs(ctx, w.org, s.ID, 10)
	if len(runs) != 1 || runs[0].Outcome != schedules.OutcomeFailed ||
		runs[0].Reason != "the template's playbook has not been registered in this organization yet" {
		t.Fatalf("runs = %+v, want the precise ErrTemplateNotReady reason, not the value-free default", runs)
	}
}

func TestListReturnsTheLatestRunPerSchedule(t *testing.T) {
	w := newWorld(t)
	s, _ := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); err != nil {
		t.Fatal(err)
	}
	list, latest, err := w.svc.List(ctx, w.org, nil)
	if err != nil || len(list) != 1 || latest[s.ID].Outcome != schedules.OutcomeTriggered {
		t.Fatalf("List = %v, %v, %v", list, latest, err)
	}
	other := uuid.Must(uuid.NewV7())
	if l, _, _ := w.svc.List(ctx, w.org, &other); len(l) != 0 {
		t.Fatalf("filtered list = %v", l)
	}
}

func TestAuditActorKindConstant(t *testing.T) {
	if audit.ActorKindSchedule != "schedule" {
		t.Fatal("ActorKindSchedule must be the literal \"schedule\"")
	}
}
