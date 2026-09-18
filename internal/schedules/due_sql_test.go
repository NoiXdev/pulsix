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
		tk := schedules.NewTicker(svc, schedules.NewDueSQLStore(d), 15*time.Second, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	if _, err := schedules.NewTicker(svc, schedules.NewDueSQLStore(d), 15*time.Second, func() time.Time { return now }, nil, nil).Tick(context.Background()); err != nil {
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
	tk := schedules.NewTicker(svc, schedules.NewDueSQLStore(d), 15*time.Second, func() time.Time { return now }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

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

// TestProcessDueDoesNotStarvePoolConnections is a regression test for fix
// round 1's Important #3 (the v0.2b pool-exhaustion shape, see
// internal/inventory/concurrency_test.go's
// TestUpdateGroupWithBastionDoesNotStarvePoolConnections, which this is
// modelled on): a batch holds one connection for its locked row and needs
// a second one for decide()'s own work (HasActiveRun, enqueue and audit
// each run on their own connection, simulated here with a plain query that
// holds a second connection briefly). With pool_max_conns=2, a concurrent
// schedule update, pause and "run now" — each needing a connection of
// their own, and two of which (update, pause) block on the very row the
// batch has locked — must all still complete within a bound instead of
// deadlocking the pool forever: the batch's own context is bounded to a
// small deadline (mirroring Ticker.Tick's batchDeadlineFactor × tick), so
// a starved second connection fails fast and the batch's transaction
// rolls back, releasing the row lock, instead of holding it (and the
// connection a blocked request needs) indefinitely.
func TestProcessDueDoesNotStarvePoolConnections(t *testing.T) {
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
	svc := schedules.NewService(st, &fakeTemplates{}, &fakeRuns{}, &fakeAudit{}, func() time.Time { return now }, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// batchDeadline mirrors Ticker.Tick's own context.WithTimeout around
	// ProcessDue (batchDeadlineFactor × tick): short on purpose, so a
	// starved second connection fails fast and the batch's transaction
	// rolls back — releasing the row lock — well before the concurrent
	// operations' own, much more generous timeouts expire.
	const batchDeadline = 2 * time.Second

	decideStarted := make(chan struct{})
	decide := func(ctx context.Context, s schedules.Schedule) schedules.Decision {
		close(decideStarted)
		// Give the concurrent update/pause goroutines below a head start
		// on the pool's one remaining connection, so they are the ones
		// blocked on the row's lock (holding that connection) by the time
		// decide() itself asks for a second connection — the actual shape
		// of the circular wait this test guards against, not merely a
		// lucky ordering where decide() always wins the race for the
		// pool's last free connection.
		time.Sleep(150 * time.Millisecond)
		// decide()'s real work (HasActiveRun, enqueue, audit) each acquire
		// their own connection from the same pool while this batch's
		// transaction still holds the row's lock on connection 1. It is
		// expected to fail here under contention — that is exactly what
		// batchDeadline is for — so no t.Error on this specific call.
		_, _ = d.Pool().Exec(ctx, "SELECT pg_sleep(1)")
		next := now.Add(time.Hour)
		return schedules.Decision{
			Run:       schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: s.OrgID, ScheduleID: s.ID, DueAt: dueAt, DecidedAt: now, Outcome: schedules.OutcomeTriggered},
			NextRunAt: &next,
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		tctx, cancel := context.WithTimeout(context.Background(), batchDeadline)
		defer cancel()
		// A deadline-exceeded error here is the batch giving up on
		// purpose, not a starvation failure — it is what lets the row
		// lock go and unblocks update/pause below.
		if _, err := schedules.NewDueSQLStore(d).ProcessDue(tctx, now, 50, decide); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			errs <- fmt.Errorf("ticker batch: %w (pool exhaustion deadlock?)", err)
		}
	}()

	<-decideStarted // the row is locked and decide() is running

	wg.Add(1)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
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
		cctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if _, err := svc.Pause(cctx, org, sch.ID); err != nil {
			errs <- fmt.Errorf("concurrent pause: %w (pool exhaustion deadlock?)", err)
		}
	}()

	// run now's schedule_runs insert takes only FOR KEY SHARE on the
	// schedule row (see TestLockDueSchedulesDoesNotBlockRunNow for that
	// claim in isolation, without the pool_max_conns=2 contention from
	// update/pause above also competing for the one spare connection).
	// Here it only needs to complete without error inside a generous
	// bound, the same as update and pause.
	wg.Add(1)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if _, err := svc.RunNow(cctx, org, sch.ID, uuid.Must(uuid.NewV7())); err != nil {
			errs <- fmt.Errorf("concurrent run now: %w (pool exhaustion deadlock?)", err)
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent operations did not complete within 15s — pool exhaustion deadlock?")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
