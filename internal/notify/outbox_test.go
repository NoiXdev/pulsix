package notify_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/notify"
)

type outboxWorld struct {
	d                    *db.DB
	org, schedule        uuid.UUID
	chA, chB, chDisabled notify.Channel
	outbox               *notify.Outbox
	svc                  *notify.Service
}

// newOutboxWorld seeds an organization with a template, a schedule and
// three channels (one disabled), and rules: failure → A and B, change → B,
// skipped → A and the disabled one.
func newOutboxWorld(t *testing.T, slug string) *outboxWorld {
	t.Helper()
	d := testdb.Open(t)
	w := &outboxWorld{d: d, org: seedOrg(t, d, slug)}
	q := gen.New(d.Pool())
	inline := "- hosts: all\n  tasks: []\n"
	pb, err := q.CreatePlaybook(ctx, gen.CreatePlaybookParams{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Inline: &inline, Source: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := q.InsertTemplate(ctx, gen.InsertTemplateParams{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Name: "patch-all", PlaybookID: pb.ID, Scope: []byte(`{}`), ExtraVars: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	sch, err := q.CreateSchedule(ctx, gen.CreateScheduleParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, TemplateID: tmpl.ID, Name: "Nightly", Mode: "cron", Cron: "0 3 * * *",
		Timezone: "UTC", Guards: []byte(`{"overlap":"skip","catch_up":{"enabled":false,"window":"1h"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.schedule = sch.ID
	w.chA, w.chB, w.chDisabled = seedChannel(t, d, w.org), seedChannel(t, d, w.org), seedChannel(t, d, w.org)
	if _, err := d.Pool().Exec(ctx, `UPDATE notification_channels SET enabled = false WHERE id = $1`, w.chDisabled.ID); err != nil {
		t.Fatal(err)
	}
	store := notify.NewSQLStore(d)
	w.svc = notify.NewService(notify.Deps{Channels: store, Deliveries: store, Rules: store, Box: fakeBox{}, Mail: &fakeMail{configured: true}})
	if _, err := w.svc.SetScheduleRules(db.WithOrgScope(ctx, w.org), w.org, w.schedule, []notify.Rule{
		{On: notify.CategoryFailure, Channels: []uuid.UUID{w.chA.ID, w.chB.ID}},
		{On: notify.CategoryChange, Channels: []uuid.UUID{w.chB.ID}},
		{On: notify.CategorySkipped, Channels: []uuid.UUID{w.chA.ID, w.chDisabled.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	w.outbox = notify.NewOutbox(d, "https://pulsix.example.test/", func() time.Time { return time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) }, rand.Reader, nil)
	return w
}

type queued struct {
	channel uuid.UUID
	key     string
	ev      notify.Event
}

func (w *outboxWorld) queued(t *testing.T) []queued {
	t.Helper()
	rows, err := w.d.Pool().Query(ctx, `SELECT channel_id, event_key, payload FROM notification_deliveries WHERE org_id = $1 ORDER BY created_at, channel_id`, w.org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []queued
	for rows.Next() {
		var q queued
		var payload []byte
		if err := rows.Scan(&q.channel, &q.key, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &q.ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, q)
	}
	return out
}

// finish reports a run group the way the job queue does: inside a write
// transaction bound to the organization.
func (w *outboxWorld) finish(t *testing.T, r jobs.RunGroupResult) {
	t.Helper()
	if err := w.d.WithWriteTx(ctx, w.org, func(ctx context.Context) error { return w.outbox.RunGroupFinished(ctx, r) }); err != nil {
		t.Fatal(err)
	}
}

func TestRunGroupFinishedQueuesOneMessagePerChannel(t *testing.T) {
	w := newOutboxWorld(t, "outbox-run")
	start := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	end := start.Add(252 * time.Second)
	first := uuid.Must(uuid.NewV7())
	r := jobs.RunGroupResult{
		OrgID: w.org, GroupID: uuid.Must(uuid.NewV7()), FirstJobID: first, Trigger: jobs.TriggerSchedule, TriggerRef: &w.schedule,
		Status: jobs.StatusFailed, Jobs: 2, Hosts: 12, HostsChanged: 9, HostsFailed: 1, StartedAt: &start, FinishedAt: &end,
	}
	w.finish(t, r)
	w.finish(t, r) // a retried finish enqueues nothing new
	got := w.queued(t)
	// failure → A, B; change → B: B gets ONE message for both.
	if len(got) != 2 {
		t.Fatalf("queued = %+v, want one message each for A and B", got)
	}
	for _, q := range got {
		if q.key != "run:"+r.GroupID.String() || q.ev.Kind != notify.EventRunFinished || q.ev.Run.Result != "failed" ||
			q.ev.Run.HostsOK != 11 || q.ev.Run.DurationSeconds != 252 || q.ev.Schedule.Name != "Nightly" || q.ev.Template.Name != "patch-all" ||
			q.ev.URL != "https://pulsix.example.test/o/outbox-run/runs/"+first.String() || len(q.ev.Categories) != 2 {
			t.Fatalf("queued = %+v", q)
		}
	}
}

func TestRunGroupFinishedFiltersByCategoryAndTrigger(t *testing.T) {
	w := newOutboxWorld(t, "outbox-filter")
	// A clean success: no rule listens to success.
	w.finish(t, jobs.RunGroupResult{OrgID: w.org, GroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerSchedule, TriggerRef: &w.schedule, Status: jobs.StatusSucceeded, Hosts: 1})
	// A manual run: schedules' rules do not apply.
	w.finish(t, jobs.RunGroupResult{OrgID: w.org, GroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerManual, Status: jobs.StatusFailed})
	if got := w.queued(t); len(got) != 0 {
		t.Fatalf("queued = %+v, want nothing", got)
	}
	// A success with changes reaches B (change) only.
	w.finish(t, jobs.RunGroupResult{OrgID: w.org, GroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerSchedule, TriggerRef: &w.schedule, Status: jobs.StatusSucceeded, Hosts: 2, HostsChanged: 1})
	if got := w.queued(t); len(got) != 1 || got[0].channel != w.chB.ID || got[0].ev.Run.Result != "succeeded" {
		t.Fatalf("queued = %+v, want B only", got)
	}
}

func TestEnqueueDecisionSkipsDisabledChannelsAndTriggeredDecisions(t *testing.T) {
	w := newOutboxWorld(t, "outbox-decision")
	decide := func(outcome string, runID uuid.UUID) {
		t.Helper()
		tx, err := w.d.Pool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := w.outbox.EnqueueDecision(ctx, tx, notify.Decision{
			OrgID: w.org, ScheduleID: w.schedule, RunID: runID, Outcome: outcome, Reason: "every targeted host is in a maintenance window",
			DueAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), DecidedAt: time.Date(2026, 9, 22, 3, 0, 1, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	decide("triggered", uuid.Must(uuid.NewV7()))
	if got := w.queued(t); len(got) != 0 {
		t.Fatalf("a triggered decision queued %+v", got)
	}
	run := uuid.Must(uuid.NewV7())
	decide("skipped_maintenance", run)
	decide("skipped_maintenance", run) // idempotent per decision
	got := w.queued(t)
	if len(got) != 1 || got[0].channel != w.chA.ID || got[0].key != "decision:"+run.String() || got[0].ev.Kind != notify.EventScheduleSkipped ||
		got[0].ev.Decision.Outcome != "skipped_maintenance" || got[0].ev.URL != "https://pulsix.example.test/o/outbox-decision/schedules?id="+w.schedule.String() {
		t.Fatalf("queued = %+v, want one skipped message to A (the disabled channel gets none)", got)
	}
	decide("failed", uuid.Must(uuid.NewV7()))
	if got := w.queued(t); len(got) != 3 {
		t.Fatalf("a failed decision reaches the failure channels A and B, queued = %d", len(got))
	}
}

func TestOutboxIgnoresADeletedSchedule(t *testing.T) {
	w := newOutboxWorld(t, "outbox-gone")
	if _, err := w.d.Pool().Exec(ctx, `DELETE FROM schedules WHERE id = $1`, w.schedule); err != nil {
		t.Fatal(err)
	}
	w.finish(t, jobs.RunGroupResult{OrgID: w.org, GroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerSchedule, TriggerRef: &w.schedule, Status: jobs.StatusFailed})
	if got := w.queued(t); len(got) != 0 {
		t.Fatalf("queued = %+v", got)
	}
}

func TestScheduleRulesRoundTrip(t *testing.T) {
	w := newOutboxWorld(t, "outbox-rules")
	scoped := db.WithOrgScope(ctx, w.org)
	views, err := w.svc.ScheduleRules(scoped, w.org, w.schedule)
	if err != nil || len(views) != 3 || views[0].On != notify.CategoryFailure || len(views[0].Channels) != 2 || views[2].On != notify.CategorySkipped {
		t.Fatalf("rules = %+v, %v", views, err)
	}
	if _, err := w.svc.SetScheduleRules(scoped, w.org, w.schedule, []notify.Rule{{On: notify.CategorySuccess, Channels: []uuid.UUID{uuid.Must(uuid.NewV7())}}}); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("an unknown channel = %v, want ErrInvalidInput", err)
	}
	if views, err := w.svc.SetScheduleRules(scoped, w.org, w.schedule, nil); err != nil || len(views) != 0 {
		t.Fatalf("clearing = %+v, %v", views, err)
	}
}

// TestSetScheduleRulesOnADeletedSchedule covers the second of
// schedule_notifications' two foreign keys: a schedule that vanished
// between the handler's read and the write is a missing schedule, not the
// unknown channel the other key reports.
func TestSetScheduleRulesOnADeletedSchedule(t *testing.T) {
	w := newOutboxWorld(t, "outbox-sched-gone")
	if _, err := w.d.Pool().Exec(ctx, `DELETE FROM schedules WHERE id = $1`, w.schedule); err != nil {
		t.Fatal(err)
	}
	_, err := w.svc.SetScheduleRules(db.WithOrgScope(ctx, w.org), w.org, w.schedule, []notify.Rule{
		{On: notify.CategoryFailure, Channels: []uuid.UUID{w.chA.ID}},
	})
	if !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("a deleted schedule = %v, want ErrNotFound", err)
	}
}

// TestRunGroupFinishedTreatsCancelledAndErrorAsFailure pins the two status
// arms the other tests do not reach. A cancelled run did not do its job, so
// the plan counts it as a failure; an errored one likewise.
func TestRunGroupFinishedTreatsCancelledAndErrorAsFailure(t *testing.T) {
	w := newOutboxWorld(t, "outbox-status")
	for _, tc := range []struct {
		status jobs.Status
		result string
	}{
		{jobs.StatusCancelled, "cancelled"},
		{jobs.StatusError, "failed"},
	} {
		group := uuid.Must(uuid.NewV7())
		w.finish(t, jobs.RunGroupResult{
			OrgID: w.org, GroupID: group, FirstJobID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerSchedule,
			TriggerRef: &w.schedule, Status: tc.status, Hosts: 3,
		})
		var got []queued
		for _, q := range w.queued(t) {
			if q.key == "run:"+group.String() {
				got = append(got, q)
			}
		}
		// failure → A and B; no host changed, so the change rule adds nobody.
		if len(got) != 2 {
			t.Fatalf("%s queued %d messages, want one each for A and B", tc.status, len(got))
		}
		for _, q := range got {
			if q.ev.Run.Result != tc.result || len(q.ev.Categories) != 1 || q.ev.Categories[0] != notify.CategoryFailure {
				t.Fatalf("%s queued %+v", tc.status, q.ev.Run)
			}
		}
	}
}

// TestNewOutboxDefaultsClockAndEntropy: a nil rand must not reach
// uuid.NewV7FromReader, because that panics inside the caller's own
// transaction — the one place a notification must never reach.
func TestNewOutboxDefaultsClockAndEntropy(t *testing.T) {
	w := newOutboxWorld(t, "outbox-defaults")
	w.outbox = notify.NewOutbox(w.d, "https://pulsix.example.test", nil, nil, nil)
	group := uuid.Must(uuid.NewV7())
	w.finish(t, jobs.RunGroupResult{
		OrgID: w.org, GroupID: group, FirstJobID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerSchedule,
		TriggerRef: &w.schedule, Status: jobs.StatusFailed, Hosts: 1,
	})
	if got := w.queued(t); len(got) != 2 || got[0].key != "run:"+group.String() {
		t.Fatalf("queued = %+v, want the failure channels reached with defaulted clock and entropy", got)
	}
}

// TestEnqueueIsTheExportedDoor: alertroutes cannot reach insert(), and must
// not write notification_deliveries itself: rendering, payload shape and
// the idempotency key all live here.
func TestEnqueueIsTheExportedDoor(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d, "alert-batch-enqueue")
	ch := seedChannel(t, d, org)
	o := notify.NewOutbox(d, "https://pulsix.example", time.Now, rand.Reader, slog.Default())

	ev := notify.Event{Kind: notify.EventAlertBatch, At: time.Now(), Org: notify.OrgRef{ID: org, Name: "Acme"},
		Alerts: &notify.AlertBatch{Count: 1, Items: []notify.AlertItem{{HostName: "db-1", Severity: "crit", Event: "opened"}}}}

	tx, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := o.Enqueue(ctx, tx, org, "alert.batch:k1", ev, []uuid.UUID{ch.ID}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// The same key twice is one delivery: the outbox's own idempotency.
	if err := o.Enqueue(ctx, tx, org, "alert.batch:k1", ev, []uuid.UUID{ch.ID}); err != nil {
		t.Fatalf("Enqueue twice: %v", err)
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM notification_deliveries WHERE org_id = $1 AND event_key = 'alert.batch:k1'`,
		org).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("deliveries = %d, want 1", n)
	}
}
