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

// recordingOutbox records every decision it is handed, notes whether it was
// ever handed no transaction at all, and answers err. It records rather
// than returns that fact, because processRow only logs an outbox error: an
// error returned from here would change nothing observable, so the test
// asserts sawNilTx directly.
type recordingOutbox struct {
	mu        sync.Mutex
	decisions []notify.Decision
	sawNilTx  bool
	err       error
}

func (o *recordingOutbox) EnqueueDecision(_ context.Context, tx pgx.Tx, d notify.Decision) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.decisions = append(o.decisions, d)
	if tx == nil {
		o.sawNilTx = true
	}
	return o.err
}

// markerOutbox writes a real row through the transaction it is handed, so a
// test can see whether that write lives inside the ticker's row savepoint.
// panicAfterWrite then blows the notification path up, which is the only
// failure that can still reach processRow's savepoint after the outbox has
// written — the outbox is the last step before the commit.
type markerOutbox struct {
	mu              sync.Mutex
	marker          uuid.UUID
	writeErr        error
	sawNilTx        bool
	panicAfterWrite bool
}

func (o *markerOutbox) EnqueueDecision(ctx context.Context, tx pgx.Tx, d notify.Decision) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if tx == nil {
		o.sawNilTx = true
		return errors.New("the outbox was handed no transaction")
	}
	_, o.writeErr = tx.Exec(ctx,
		`INSERT INTO schedule_runs (id, org_id, schedule_id, due_at, decided_at, outcome, reason)
		 VALUES ($1, $2, $3, $4, $5, 'missed', 'outbox marker')`,
		o.marker, d.OrgID, d.ScheduleID, d.DueAt, d.DecidedAt)
	if o.writeErr != nil {
		return o.writeErr
	}
	if o.panicAfterWrite {
		panic("the notification path blew up after writing")
	}
	return nil
}

// dueSchedule seeds one cron schedule that is already due, with a run
// active so the overlap guard decides it without starting a run, and
// returns the ticker wired to outbox.
func dueSchedule(t *testing.T, outbox schedules.DecisionOutbox) (*db.DB, uuid.UUID, schedules.Schedule, *schedules.SQLStore, *schedules.Ticker, time.Time) {
	t.Helper()
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
	due.SetOutbox(outbox)
	return d, org, sch, st, schedules.NewTicker(svc, due, 15*time.Second, func() time.Time { return now }, nil, quiet), dueAt
}

func TestTickerHandsEveryDecisionToTheOutbox(t *testing.T) {
	for _, outboxErr := range []error{nil, errors.New("outbox down")} {
		outbox := &recordingOutbox{err: outboxErr}
		_, org, sch, st, ticker, dueAt := dueSchedule(t, outbox)
		if _, err := ticker.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if len(outbox.decisions) != 1 {
			t.Fatalf("decisions = %+v", outbox.decisions)
		}
		if outbox.sawNilTx {
			t.Fatal("the outbox was handed no transaction")
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

// TestDecisionOutboxWriteSharesTheRowSavepoint is the transactional-outbox
// property itself: what the outbox writes commits with the decision or not
// at all. The fake writes a real row through the transaction it is handed,
// so the test observes the binding instead of trusting it — a nil
// transaction cannot write at all, and a transaction of the outbox's own
// would leave its marker behind when the row's savepoint rolls back.
func TestDecisionOutboxWriteSharesTheRowSavepoint(t *testing.T) {
	for _, tc := range []struct {
		name       string
		panicAfter bool
		want       bool // the marker and the decision both survive
	}{
		{name: "the row commits and takes the outbox write with it", want: true},
		{name: "the row's savepoint rolls back and takes the outbox write with it", panicAfter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outbox := &markerOutbox{marker: uuid.Must(uuid.NewV7()), panicAfterWrite: tc.panicAfter}
			d, org, sch, st, ticker, _ := dueSchedule(t, outbox)
			if _, err := ticker.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if outbox.sawNilTx {
				t.Fatal("the outbox was handed no transaction")
			}
			if outbox.writeErr != nil {
				t.Fatalf("the outbox could not write through the transaction it was handed: %v", outbox.writeErr)
			}
			var markers int
			if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM schedule_runs WHERE id = $1`, outbox.marker).Scan(&markers); err != nil {
				t.Fatal(err)
			}
			decisions, err := st.Runs(db.WithOrgScope(ctx, org), org, sch.ID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want {
				// The decision, and the marker the outbox wrote beside it.
				if markers != 1 || len(decisions) != 2 {
					t.Fatalf("markers = %d, decisions = %d, want the outbox write committed with the decision", markers, len(decisions))
				}
				return
			}
			// The savepoint rolled back: neither the marker nor the decision
			// survives, and the schedule is still due for the next tick.
			if markers != 0 || len(decisions) != 0 {
				t.Fatalf("markers = %d, decisions = %d, want the outbox write rolled back with the decision", markers, len(decisions))
			}
			after, err := st.Get(db.WithOrgScope(ctx, org), org, sch.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.NextRunAt == nil || !after.NextRunAt.Before(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)) {
				t.Fatalf("next_run_at = %v, want the row still due", after.NextRunAt)
			}
		})
	}
}
