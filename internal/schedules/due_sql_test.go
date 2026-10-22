package schedules_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
)

// TestTwoTickersDecideEachOccurrenceOnce: 20 due schedules, two tickers
// over two stores ticking concurrently — every schedule gets exactly one
// run row and one enqueue; next_run_at moves past now.
func TestTwoTickersDecideEachOccurrenceOnce(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-concurrent")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-5 * time.Second)
	for i := 0; i < 20; i++ {
		s := schedules.Schedule{
			ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "s" + uuid.NewString()[:8],
			Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
			Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt,
		}
		if _, err := st.Create(scoped, s); err != nil {
			t.Fatal(err)
		}
	}
	runs := &fakeRuns{}
	svc := schedules.NewService(st, &fakeTemplates{}, runs, &fakeAudit{}, func() time.Time { return now }, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		tk := schedules.NewTicker(svc, schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))), 15*time.Second, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				if _, err := tk.Tick(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if len(runs.requests) != 20 {
		t.Fatalf("enqueues = %d, want 20", len(runs.requests))
	}
	var count int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE org_id = $1`, org).Scan(&count); err != nil || count != 20 {
		t.Fatalf("runs = %d, %v", count, err)
	}
	list, err := st.List(scoped, org, nil)
	if err != nil || len(list) != 20 {
		t.Fatalf("list = %d schedules, %v", len(list), err)
	}
	for _, s := range list {
		if !s.NextRunAt.Equal(time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)) || s.LastRunAt == nil || s.Revision != 1 || s.UpdatedAt != s.CreatedAt {
			t.Fatalf("schedule after tick = %+v (AdvanceSchedule must never touch revision or updated_at)", s)
		}
	}
}

// TestSkippedMaintenanceRunRoundTripsThroughTheStore writes and reads back
// a skipped_maintenance row through the real store: the widened named
// CHECK (00032_maintenance_windows.sql) must actually accept the value,
// not just read correctly by inspection — a future migration narrowing it
// again would fail this at insert time.
func TestSkippedMaintenanceRunRoundTripsThroughTheStore(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-skip-maint")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	s, err := st.Create(scoped, schedules.Schedule{ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "maint-skip",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}, Maintenance: schedules.MaintenanceSkip}, NextRunAt: &now})
	if err != nil {
		t.Fatal(err)
	}
	run := schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: org, ScheduleID: s.ID, DueAt: now, DecidedAt: now,
		Outcome: schedules.OutcomeSkippedMaintenance, Reason: "1 targeted host(s) in a maintenance window"}
	if err := st.RecordRun(scoped, run); err != nil {
		t.Fatalf("RecordRun(skipped_maintenance) = %v, want the widened CHECK to accept it", err)
	}
	runs, err := st.Runs(scoped, org, s.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Outcome != schedules.OutcomeSkippedMaintenance || runs[0].Reason != run.Reason {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
}

func TestProcessDueTrimsRunsTo500(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-trim")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	s, err := st.Create(scoped, schedules.Schedule{ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "trim",
		Mode: schedules.ModeCron, Cron: "* * * * *", Timezone: "UTC", Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &now})
	if err != nil {
		t.Fatal(err)
	}
	oldestID, newestID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for i := 0; i < 505; i++ {
		at := now.Add(-time.Duration(1000-i) * time.Minute)
		id := uuid.Must(uuid.NewV7())
		switch i {
		case 0:
			id = oldestID
		case 504:
			id = newestID
		}
		if err := st.RecordRun(scoped, schedules.Run{ID: id, OrgID: org, ScheduleID: s.ID, DueAt: at, DecidedAt: at, Outcome: schedules.OutcomeMissed}); err != nil {
			t.Fatal(err)
		}
	}
	svc := schedules.NewService(st, &fakeTemplates{}, &fakeRuns{}, &fakeAudit{}, func() time.Time { return now }, rand.Reader, nil)
	if _, err := schedules.NewTicker(svc, schedules.NewDueSQLStore(d, rand.Reader, nil), 15*time.Second, func() time.Time { return now }, nil, nil).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, s.ID).Scan(&count); err != nil || count != 500 {
		t.Fatalf("runs after trim = %d, %v", count, err)
	}
	// The trim keeps the newest rows by decided_at, not an arbitrary 500:
	// the oldest inserted run must be gone and the newest must survive. A
	// reversed ORDER BY in TrimScheduleRuns would still pass a bare count
	// check but fail this one.
	var oldestExists, newestExists bool
	if err := d.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_runs WHERE id = $1)`, oldestID).Scan(&oldestExists); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_runs WHERE id = $1)`, newestID).Scan(&newestExists); err != nil {
		t.Fatal(err)
	}
	if oldestExists || !newestExists {
		t.Fatalf("trim survivors wrong: oldest exists=%v (want false), newest exists=%v (want true)", oldestExists, newestExists)
	}
}

// TestProcessDueIsolatesACorruptRowFromHealthyRows is fix round 1's
// Important #2: a row whose stored guards no longer decode (seeded
// directly here, bypassing the service, which would never write a value
// that doesn't decode — a hand-edited row, or a stored shape from a
// version whose struct has since changed) must not abort the batch after
// the healthy row next to it was already enqueued. The healthy row fires
// exactly once, the corrupt row is stopped (next_run_at cleared, one
// failed run recorded) instead of retried forever, and a second tick
// fires nothing more.
func TestProcessDueIsolatesACorruptRowFromHealthyRows(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-corrupt")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)

	healthy, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "healthy",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seeded directly through the raw pool with guards that are valid
	// jsonb but do not decode into schedules.Guards (a JSON string, not an
	// object) — the same technique seedTemplate uses to bypass RLS for
	// fixtures.
	q := gen.New(d.Pool())
	corrupt, err := q.CreateSchedule(ctx, gen.CreateScheduleParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "corrupt", Mode: string(schedules.ModeCron),
		Cron: "0 * * * *", Timezone: "UTC", Guards: []byte(`"not an object"`), NextRunAt: db.NullTime(&dueAt),
	})
	if err != nil {
		t.Fatal(err)
	}

	runs := &fakeRuns{}
	svc := schedules.NewService(st, &fakeTemplates{}, runs, &fakeAudit{}, func() time.Time { return now }, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tk := schedules.NewTicker(svc, schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))), 15*time.Second, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	n, err := tk.Tick(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	if len(runs.requests) != 1 {
		t.Fatalf("enqueued = %d, want 1 (the healthy schedule only)", len(runs.requests))
	}

	gotHealthy, err := st.Get(scoped, org, healthy.ID)
	if err != nil || gotHealthy.NextRunAt == nil || !gotHealthy.NextRunAt.Equal(time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("healthy schedule after tick = %+v, %v", gotHealthy, err)
	}

	var corruptNextRunAt *time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT next_run_at FROM schedules WHERE id = $1`, corrupt.ID).Scan(&corruptNextRunAt); err != nil {
		t.Fatal(err)
	}
	if corruptNextRunAt != nil {
		t.Fatalf("corrupt schedule must be stopped (next_run_at NULL), got %v", corruptNextRunAt)
	}
	var corruptFailedRuns int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1 AND outcome = 'failed'`, corrupt.ID).Scan(&corruptFailedRuns); err != nil || corruptFailedRuns != 1 {
		t.Fatalf("corrupt schedule's failed run = %d, %v", corruptFailedRuns, err)
	}

	// A second tick fires nothing more: the healthy schedule already
	// advanced past due, and the corrupt one is stopped, not retried.
	if _, err := tk.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.requests) != 1 {
		t.Fatalf("enqueued after second tick = %d, want still 1", len(runs.requests))
	}
}

// TestProcessDueKeepsAlreadyEnqueuedRowsWhenTheBatchDeadlineIsExceeded is
// fix round 2's New Important: the batch's decide/enqueue deadline must
// not reach the write-back of a row decide has already finished — by
// then its enqueue may already have happened, and discarding the write
// (by letting the deadline also poison the savepoint and the final
// commit) would silently re-fire it on every following tick. Two due
// rows, ordered so row1 is decided (quickly) before row2; row2's decide
// blocks well past the batch's own deadline. row1 must still be recorded
// exactly once, and a second tick must not enqueue it again.
func TestProcessDueKeepsAlreadyEnqueuedRowsWhenTheBatchDeadlineIsExceeded(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-overrun")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt1 := now.Add(-2 * time.Second)
	dueAt2 := now.Add(-1 * time.Second) // sorts after row1 by next_run_at

	row1, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "row1",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt1,
	})
	if err != nil {
		t.Fatal(err)
	}
	row2, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "row2",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt2,
	})
	if err != nil {
		t.Fatal(err)
	}

	const deadline = 100 * time.Millisecond
	nextRun := now.Add(time.Hour)
	decide := func(ctx context.Context, s schedules.Schedule) schedules.Decision {
		if s.ID == row2.ID {
			// Blocks well past the batch's own deadline — a stand-in for
			// a stuck enqueue under pool contention, which is exactly
			// what the deadline exists to bound. It ignores ctx on
			// purpose: a decide that itself respects cancellation would
			// return quickly and not exercise this path.
			time.Sleep(5 * deadline)
		}
		return schedules.Decision{
			Run:       schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: s.OrgID, ScheduleID: s.ID, DueAt: now, DecidedAt: now, Outcome: schedules.OutcomeTriggered},
			NextRunAt: &nextRun,
		}
	}

	tctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	if _, err := schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))).ProcessDue(tctx, now, 50, decide); err != nil {
		t.Fatalf("first batch: %v (row1's already-enqueued write must survive a deadline hit on row2, not fail the whole batch)", err)
	}

	var row1Runs int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, row1.ID).Scan(&row1Runs); err != nil || row1Runs != 1 {
		t.Fatalf("row1 runs after the overrun batch = %d, %v, want exactly 1", row1Runs, err)
	}
	var row1Next *time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT next_run_at FROM schedules WHERE id = $1`, row1.ID).Scan(&row1Next); err != nil {
		t.Fatal(err)
	}
	if row1Next == nil || !row1Next.Equal(nextRun) {
		t.Fatalf("row1 next_run_at = %v, want %v (it must have advanced, not been rolled back)", row1Next, nextRun)
	}

	// A second tick, with plenty of time, must not enqueue row1 again:
	// its due occurrence was already recorded and advanced by the first,
	// overrun batch.
	if _, err := schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))).ProcessDue(context.Background(), now, 50, decide); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, row1.ID).Scan(&row1Runs); err != nil || row1Runs != 1 {
		t.Fatalf("row1 runs after the second tick = %d, %v, want still 1 (it must not fire again)", row1Runs, err)
	}
}

// TestProcessRowRecoversAPanicOutsideDecide exercises processRow's own
// recover directly (fix round 1, Important #1's belt to Decide's
// suspenders — round 2's re-review found no test drove a panic outside
// Decide: both existing panic tests go through Service.Decide, so only
// its own recover was ever exercised). decide here is a raw callback, not
// Service.Decide, so a panic in it is caught only by processRow itself.
// Two due rows, ordered so the poisoned one is decided first: the healthy
// row next to it must still fire exactly once, and the poisoned one is
// simply left due (no run recorded, next_run_at unchanged) rather than
// aborting the batch.
func TestProcessRowRecoversAPanicOutsideDecide(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-rowpanic")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt1 := now.Add(-2 * time.Second)
	dueAt2 := now.Add(-1 * time.Second) // sorts after the poisoned row

	poison, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "poison",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt1,
	})
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "healthy",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt2,
	})
	if err != nil {
		t.Fatal(err)
	}

	nextRun := now.Add(time.Hour)
	decide := func(ctx context.Context, s schedules.Schedule) schedules.Decision {
		if s.ID == poison.ID {
			panic("boom: decide panicked outside Decide")
		}
		return schedules.Decision{
			Run:       schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: s.OrgID, ScheduleID: s.ID, DueAt: now, DecidedAt: now, Outcome: schedules.OutcomeTriggered},
			NextRunAt: &nextRun,
		}
	}

	n, err := schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))).ProcessDue(context.Background(), now, 50, decide)
	if err != nil || n != 2 {
		t.Fatalf("ProcessDue = %d, %v", n, err)
	}

	var healthyRuns, poisonRuns int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, healthy.ID).Scan(&healthyRuns); err != nil || healthyRuns != 1 {
		t.Fatalf("healthy runs = %d, %v, want exactly 1", healthyRuns, err)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, poison.ID).Scan(&poisonRuns); err != nil || poisonRuns != 0 {
		t.Fatalf("poisoned schedule's runs = %d, %v, want 0 (its panic escaped processRow's inner writes, so nothing was ever recorded)", poisonRuns, err)
	}
	var poisonNext *time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT next_run_at FROM schedules WHERE id = $1`, poison.ID).Scan(&poisonNext); err != nil {
		t.Fatal(err)
	}
	if poisonNext == nil || !poisonNext.Equal(dueAt1) {
		t.Fatalf("poisoned schedule's next_run_at = %v, want unchanged (%v): it stays due for the next tick", poisonNext, dueAt1)
	}
}

// TestRetriedOccurrenceAfterAWriteBackFailureEnqueuesOnlyOnce is backlog
// 2.60's central regression: due_sql.go's processRow commits decide's
// enqueue (through the real Service.Decide, so occurrenceKey's wiring is
// exercised exactly as production runs it) BEFORE its own savepoint ever
// opens. If something fails between those two points — here, a panic
// standing in for a write error or a crashed manager, the same class
// processRow's own recover already catches — the row is left due with no
// schedule_runs row and no next_run_at advance to show for the enqueue that
// already happened. The next tick decides the SAME occurrence again (same
// schedule, same still-unchanged next_run_at) and must not create a second
// run group for it: that would be a duplicate playbook run against the
// customer's servers, recorded as a fresh "triggered" decision for work
// that, from the jobs service's point of view, already ran.
//
// fakeRuns models the real ledger (job_idempotency_keys) at the RunEnqueuer
// boundary: the real mechanism itself — the ON CONFLICT round trip and its
// org scoping — is covered directly against Postgres in
// internal/jobs/idempotency_store_test.go; this test is schedules' own
// responsibility, that decide.go/service.go compute and send the SAME key
// for a retried decision of an unchanged due occurrence.
func TestRetriedOccurrenceAfterAWriteBackFailureEnqueuesOnlyOnce(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-retry-dup")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)

	sch, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "retry-dup",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	runs := &fakeRuns{}
	svc := schedules.NewService(st, &fakeTemplates{}, runs, &fakeAudit{}, func() time.Time { return now }, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))

	attempt := 0
	decide := func(ctx context.Context, s schedules.Schedule) schedules.Decision {
		attempt++
		d := svc.Decide(ctx, s, now, 2*15*time.Second)
		if attempt == 1 {
			// Stands in for a write-back failure AFTER decide's own enqueue
			// already committed (a write error, a crashed manager):
			// processRow's own recover (due_sql.go) catches this exactly
			// like it would catch a real one, leaving the row due with
			// nothing written back.
			panic("simulated write-back failure after a successful enqueue")
		}
		return d
	}

	if _, err := store.ProcessDue(context.Background(), now, 50, decide); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	var firstRuns int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, sch.ID).Scan(&firstRuns); err != nil || firstRuns != 0 {
		t.Fatalf("schedule_runs after the panicked write-back = %d, %v, want 0 (nothing written back)", firstRuns, err)
	}
	var nextAfterFirst *time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT next_run_at FROM schedules WHERE id = $1`, sch.ID).Scan(&nextAfterFirst); err != nil {
		t.Fatal(err)
	}
	if nextAfterFirst == nil || !nextAfterFirst.Equal(dueAt) {
		t.Fatalf("next_run_at after the panicked write-back = %v, want unchanged (%v): still due for the retry", nextAfterFirst, dueAt)
	}
	if len(runs.requests) != 1 || runs.requests[0].IdempotencyKey == "" {
		t.Fatalf("enqueue requests after the first attempt = %+v, want exactly one with a non-empty IdempotencyKey", runs.requests)
	}

	// The retry: the row is still due, so the next tick decides the SAME
	// occurrence again.
	if _, err := store.ProcessDue(context.Background(), now, 50, decide); err != nil {
		t.Fatalf("second batch: %v", err)
	}
	var secondRuns int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1`, sch.ID).Scan(&secondRuns); err != nil || secondRuns != 1 {
		t.Fatalf("schedule_runs after the retry = %d, %v, want exactly 1 (the retry's own write-back, now successful)", secondRuns, err)
	}

	if len(runs.requests) != 2 {
		t.Fatalf("enqueue requests = %d, want 2 (the panic-interrupted attempt, then the retry)", len(runs.requests))
	}
	if runs.requests[1].IdempotencyKey != runs.requests[0].IdempotencyKey {
		t.Fatalf("idempotency keys = %q then %q, want equal: same schedule, same due instant, since the write-back never advanced it", runs.requests[0].IdempotencyKey, runs.requests[1].IdempotencyKey)
	}
	// The assertion that actually matters: however many times enqueue was
	// CALLED, only one run group ever exists for this occurrence — the
	// retry replayed the first attempt's, it did not mint a second one.
	if len(runs.groups) != 1 {
		t.Fatalf("distinct run groups for the occurrence = %d, want 1 — a second one is the duplicate playbook run backlog 2.60 exists to prevent", len(runs.groups))
	}
}

// TestProcessDueReportsRowsDecidedNotRowsLockedWhenTheDeadlineCutsItShort is
// backlog 2.60's ticker-spin fix: Ticker.Tick (ticker.go) treats a batch
// that comes back with fewer than batchSize decided as "no more work, wait
// for the next tick interval" and a full batchSize as "there may be more,
// start another batch immediately". Before this fix, ProcessDue reported
// how many rows it had LOCKED, not how many it actually got to decide
// before its own deadline — so a batch that locked a full, busy-looking
// batchSize rows but only managed to decide the first one or two before the
// deadline cut it short still reported batchSize, indistinguishable from a
// batch that genuinely finished one. Tick then started a fresh batch at
// once instead of backing off, hammering whatever made the first batch slow
// (a starved pool, an overloaded database) instead of giving it the rest of
// the tick interval to recover.
//
// Two due rows and limit == 2 (standing in for batchSize): row1's own
// decide sleeps well past the deadline — guaranteeing ctx is already done
// by the time the loop reaches row2's own ctx.Err() check, deterministically,
// rather than racing real DB round-trip latency against the deadline — so
// the batch locks 2 rows but only ever gets to decide 1. The fix must
// report 1, not 2; row2 must never even be decided.
func TestProcessDueReportsRowsDecidedNotRowsLockedWhenTheDeadlineCutsItShort(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-spin")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt1 := now.Add(-2 * time.Second)
	dueAt2 := now.Add(-1 * time.Second) // sorts after row1 by next_run_at

	row1, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "spin-row1",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "spin-row2",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt2,
	}); err != nil {
		t.Fatal(err)
	}

	const deadline = 100 * time.Millisecond
	nextRun := now.Add(time.Hour)
	decide := func(ctx context.Context, s schedules.Schedule) schedules.Decision {
		if s.ID == row1.ID {
			// Ignoring ctx on purpose, like the equivalent sleep in
			// TestProcessDueKeepsAlreadyEnqueuedRowsWhenTheBatchDeadlineIsExceeded:
			// by the time this returns, real wall-clock time has
			// definitely passed the deadline, so row2's own ctx.Err()
			// check (the top of ProcessDue's loop) is guaranteed to see it
			// expired — no race against how long row1's own write-back
			// happens to take.
			time.Sleep(3 * deadline)
		} else {
			t.Error("row2 must never be decided: the deadline should have stopped the loop before reaching it")
		}
		return schedules.Decision{
			Run:       schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: s.OrgID, ScheduleID: s.ID, DueAt: now, DecidedAt: now, Outcome: schedules.OutcomeTriggered},
			NextRunAt: &nextRun,
		}
	}

	tctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	n, err := schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))).ProcessDue(tctx, now, 2, decide)
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue reported %d rows decided, want 1 (it locked 2, but the deadline cut the batch short after row1) — Ticker.Tick's own \"n < batchSize\" check relies on this to avoid starting another batch immediately", n)
	}
}

// TestLockDueSchedulesDoesNotBlockRunNow isolates the lock-mode half of
// fix round 1's Important #3: FOR NO KEY UPDATE (unlike the previous FOR
// UPDATE) does not conflict with the FOR KEY SHARE a schedule_runs
// insert's FK check takes, so "run now" on a row the ticker currently has
// locked must complete immediately instead of waiting for that lock to be
// released.
func TestLockDueSchedulesDoesNotBlockRunNow(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-lockmode")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	sch, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "lockmode",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := schedules.NewService(st, &fakeTemplates{}, &fakeRuns{}, &fakeAudit{}, func() time.Time { return now }, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Hold the row locked exactly the way LockDueSchedules itself does —
	// the real generated query, not a hand-written stand-in that could
	// silently drift from whatever lock mode schedules.sql actually
	// specifies — on its own connection, released only when the test ends.
	tx, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // no-op once the test ends; releases the lock either way
	if _, err := gen.New(tx).LockDueSchedules(ctx, gen.LockDueSchedulesParams{Now: now, Lim: 50}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := svc.RunNow(rctx, org, sch.ID, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("run now took %s while the row was locked FOR NO KEY UPDATE; its FK check must not block on it", elapsed)
	}
}

// blockingRuns is a RunEnqueuer whose EnqueueRunWith genuinely needs a
// pool connection of its own (a real query against d, not an in-process
// fake) and, after signalling started, waits briefly before asking for
// it — giving the concurrent update/pause goroutines in
// TestTickerDoesNotStarvePoolConnections a head start on the pool's one
// spare connection, so they are the ones blocked on the ticker's row lock
// (holding that connection) by the time the enqueue itself asks for a
// connection. That is the actual shape of the circular wait fix round 1's
// Important #3 guards against — not merely a lucky ordering where the
// enqueue always wins the race for the pool's last free connection.
//
// Only the first call — the ticker's, which the test waits for before it
// starts anything else — stalls. Later calls (the concurrent run now's own
// enqueue) still take a real pool connection but return at once: they are
// not what the test bounds, and with the batch deadline's 10s floor a
// second stall would outlast their own contexts.
type blockingRuns struct {
	d       *db.DB
	started chan struct{}
	once    sync.Once
}

func (b *blockingRuns) HasActiveRun(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (b *blockingRuns) RunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (b *blockingRuns) GroupSummaries(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]jobs.GroupSummary, error) {
	return nil, nil
}

func (b *blockingRuns) EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	first := false
	b.once.Do(func() { close(b.started); first = true })
	query := "SELECT 1"
	if first {
		time.Sleep(150 * time.Millisecond)
		// A long duration, well past the batch deadline: under the old
		// FOR UPDATE lock mode with no batch deadline, what actually
		// stalls this call forever is never acquiring a connection at
		// all (see the type's own doc comment), not this query's own
		// runtime — so its exact length only matters for demonstrating
		// that, once a connection is acquired under the fix, the batch's
		// own deadline (its 10s floor here) cancels it well before it
		// would otherwise finish.
		query = "SELECT pg_sleep(60)"
	}
	if _, err := b.d.Pool().Exec(ctx, query); err != nil {
		return nil, err
	}
	return []jobs.Job{{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TemplateID: &templateID, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: req.Trigger}}, nil
}

// TestTickerDoesNotStarvePoolConnections is fix round 2's rewrite of
// Important #3's regression test. The previous version supplied its own
// context.WithTimeout directly around ProcessDue and asserted run now's
// completion against that same test-local bound; the re-review found it
// passed unchanged with the lock-mode fix reverted, with the deadline
// removed from Ticker.Tick, and even at 66d0210 (neither fix present) —
// it never actually drove Ticker.Tick's own bound, so it never exercised
// production's fix at all.
//
// This version does: it builds a real Ticker with a short real tick
// (500ms, so Tick's own deadline is its minBatchDeadline floor, 10s) over
// pool_max_conns=2, and calls Tick(context.Background()) directly — no
// test-supplied deadline of its own — so whatever bounds it is Tick's own
// internal logic. It enqueues through blockingRuns, which genuinely needs
// a second pool connection (not an in-process fake indifferent to the
// pool). Concurrently, a schedule update, pause and run now — each
// through the real service and store — target the very row the ticker
// has locked. Update and pause take an update-strength lock and so block
// on the ticker's row lock regardless of lock mode; run now's
// schedule_runs insert only takes FOR KEY SHARE, which FOR NO KEY UPDATE
// (unlike the old FOR UPDATE) does not conflict with.
//
// With the old FOR UPDATE lock mode and no deadline (verified below by
// reverting both), this deadlocks: update and pause grab the pool's one
// spare connection first and then block on the ticker's lock, so when the
// enqueue itself asks for a connection there is none left — the ticker
// can never finish deciding the row, so it never releases the lock, so
// update and pause never get theirs either. With the fix, the stuck
// enqueue is cancelled by Tick's own deadline, the batch still commits
// (fix round 2, New Important) and releases the lock, and everything
// completes shortly after that deadline.
func TestTickerDoesNotStarvePoolConnections(t *testing.T) {
	url := testdb.URL(t)
	d, err := db.OpenAsRole(context.Background(), url+"&pool_max_conns=2", "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	org, templateID := seedTemplate(t, d, "sched-pool")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	sch, err := st.Create(scoped, schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "pool",
		Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		Guards: schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	runs := &blockingRuns{d: d, started: make(chan struct{})}
	svc := schedules.NewService(st, &fakeTemplates{}, runs, &fakeAudit{}, func() time.Time { return now }, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	const tick = 500 * time.Millisecond
	tk := schedules.NewTicker(svc, schedules.NewDueSQLStore(d, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))), tick, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	tickDone := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		if _, err := tk.Tick(context.Background()); err != nil {
			t.Errorf("Tick: %v", err)
		}
		tickDone <- time.Since(start)
	}()

	select {
	case <-runs.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the ticker never reached its enqueue — setup is broken, not the deadlock under test")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		_, err := svc.Update(cctx, org, sch.ID, sch.Revision, schedules.Input{
			TemplateID: templateID, Name: sch.Name, Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
		})
		// A revision mismatch is an ordinary race against the pause
		// goroutine below over the same row, not a starvation failure.
		if err != nil && !errors.Is(err, schedules.ErrRevisionMismatch) {
			errs <- fmt.Errorf("concurrent update: %w (pool exhaustion deadlock?)", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if _, err := svc.Pause(cctx, org, sch.ID); err != nil {
			errs <- fmt.Errorf("concurrent pause: %w (pool exhaustion deadlock?)", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if _, err := svc.RunNow(cctx, org, sch.ID, uuid.Must(uuid.NewV7())); err != nil {
			errs <- fmt.Errorf("concurrent run now: %w (pool exhaustion deadlock?)", err)
		}
	}()

	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case <-time.After(45 * time.Second):
		t.Fatal("update/pause/run now did not complete within 45s — pool exhaustion deadlock?")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	select {
	case elapsed := <-tickDone:
		// 4 × 500ms is under the floor, so the deadline is 10s: a stuck
		// enqueue must fail within that bound, not merely "eventually"
		// (and well before its 60s query would finish on its own).
		if elapsed > 15*time.Second {
			t.Errorf("Tick took %s, want close to its own 10s deadline (minBatchDeadline) — a starved enqueue must fail fast, not hang", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Tick did not return within 10s of the concurrent operations finishing — pool exhaustion deadlock?")
	}
}
