package schedules_test

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
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
	list, _ := st.List(scoped, org, nil)
	for _, s := range list {
		if !s.NextRunAt.Equal(time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)) || s.LastRunAt == nil || s.Revision != 1 {
			t.Fatalf("schedule after tick = %+v", s)
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
	for i := 0; i < 505; i++ {
		at := now.Add(-time.Duration(1000-i) * time.Minute)
		if err := st.RecordRun(scoped, schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: org, ScheduleID: s.ID, DueAt: at, DecidedAt: at, Outcome: schedules.OutcomeMissed}); err != nil {
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
}
