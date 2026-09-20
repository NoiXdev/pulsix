package schedules_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/schedules"
)

// recordingOutbox records every decision it is handed, checks it got the
// row's savepoint, and answers err.
type recordingOutbox struct {
	mu        sync.Mutex
	decisions []notify.Decision
	err       error
}

func (o *recordingOutbox) EnqueueDecision(ctx context.Context, tx pgx.Tx, d notify.Decision) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.decisions = append(o.decisions, d)
	if tx == nil {
		return errors.New("no transaction")
	}
	return o.err
}

func TestTickerHandsEveryDecisionToTheOutbox(t *testing.T) {
	for _, outboxErr := range []error{nil, errors.New("outbox down")} {
		d := testdb.Open(t)
		org, templateID := seedTemplate(t, d, "sched-outbox-"+uuid.NewString()[:6])
		st := schedules.NewSQLStore(d)
		now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
		dueAt := now.Add(-5 * time.Second)
		sch, err := st.Create(db.WithOrgScope(ctx, org), schedules.Schedule{
			ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "outboxed", Mode: schedules.ModeCron, Cron: "0 * * * *", Timezone: "UTC",
			Guards: schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}}, NextRunAt: &dueAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
		runs := &fakeRuns{active: true} // overlap skip: a decision without a run
		svc := schedules.NewService(st, &fakeTemplates{}, runs, &fakeAudit{}, func() time.Time { return now }, rand.Reader, quiet)
		due := schedules.NewDueSQLStore(d, rand.Reader, quiet)
		outbox := &recordingOutbox{err: outboxErr}
		due.SetOutbox(outbox)
		if _, err := schedules.NewTicker(svc, due, 15*time.Second, func() time.Time { return now }, nil, quiet).Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if len(outbox.decisions) != 1 {
			t.Fatalf("decisions = %+v", outbox.decisions)
		}
		got := outbox.decisions[0]
		if got.OrgID != org || got.ScheduleID != sch.ID || got.Outcome != "skipped_overlap" || got.RunID == uuid.Nil || !got.DueAt.Equal(dueAt) {
			t.Fatalf("decision = %+v", got)
		}
		// The decision is recorded and the schedule advanced even when the
		// outbox failed.
		runsList, err := st.Runs(db.WithOrgScope(ctx, org), org, sch.ID, 10)
		if err != nil || len(runsList) != 1 || runsList[0].ID != got.RunID {
			t.Fatalf("outbox error %v: runs = %+v, %v", outboxErr, runsList, err)
		}
	}
}
