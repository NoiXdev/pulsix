// The alert detail against a real database (backlog 3.13b, spec "4. Alert
// detail"): Execute writes it to the alert row and to the events of an
// opening or a severity change, never to a close; the reads carry it back;
// and a lookup that fails inside its savepoint leaves the evaluator's
// transaction usable — Review Focus 5.
package alerts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// eventDetails returns kind → detail ("<nil>" when NULL) for alertID's
// events, read through q.
func eventDetails(t *testing.T, q gen.DBTX, alertID uuid.UUID) map[string]string {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT kind, detail FROM alert_events WHERE alert_id = $1`, alertID)
	if err != nil {
		t.Fatalf("query alert_events: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind string
		var detail *string
		if err := rows.Scan(&kind, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[kind] = fmt.Sprint(deref(detail))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func deref(s *string) any {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func alertDetail(t *testing.T, q gen.DBTX, id uuid.UUID) string {
	t.Helper()
	var detail *string
	if err := q.QueryRow(context.Background(), `SELECT detail FROM alerts WHERE id = $1`, id).Scan(&detail); err != nil {
		t.Fatalf("read alert detail: %v", err)
	}
	return fmt.Sprint(deref(detail))
}

func TestExecuteWritesTheDetailToTheAlertAndItsEvents(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	tx := beginTx(t, d)
	sub := tuple(host, "web", "home", "http.assertion")
	at := time.Now().UTC().Truncate(time.Microsecond)

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: at, Severity: rules.StateWarn, Detail: "status == 200 — saw 302"},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d", err, len(open))
	}
	id := open[0].ID
	if open[0].Detail != "status == 200 — saw 302" {
		t.Errorf("OpenForHost detail = %q", open[0].Detail)
	}

	// A rise that voids the acknowledgement: both of its events carry the
	// new detail, and so does the alert.
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: at.Add(time.Minute), Severity: rules.StateCrit, VoidsAck: true, Detail: "status == 200 — saw 503"},
	}); err != nil {
		t.Fatalf("execute severity: %v", err)
	}
	if got := alertDetail(t, tx, id); got != "status == 200 — saw 503" {
		t.Errorf("alert detail after the rise = %q", got)
	}
	// A fall without a detail clears the alert's (it is the latest
	// capture) and writes no event.
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: at.Add(2 * time.Minute), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute fall: %v", err)
	}
	if got := alertDetail(t, tx, id); got != "<nil>" {
		t.Errorf("alert detail after a fall without one = %q, want NULL", got)
	}
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionFlapped, AlertID: id, At: at.Add(3 * time.Minute)},
		{Kind: alerts.ActionClose, AlertID: id, At: at.Add(4 * time.Minute), Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute flap and close: %v", err)
	}

	want := map[string]string{
		"opened":          "status == 200 — saw 302",
		"severity_raised": "status == 200 — saw 503",
		"ack_voided":      "status == 200 — saw 503",
		"closed":          "<nil>",
	}
	got := eventDetails(t, tx, id)
	for kind, w := range want {
		if got[kind] != w {
			t.Errorf("%s event detail = %q, want %q", kind, got[kind], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("events = %v, want exactly %v", got, want)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestReadsCarryTheDetail(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: tuple(host, "web", "", "http.up"), At: time.Now().UTC(), Severity: rules.StateCrit, Detail: "timeout"},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	svc := alerts.NewService(st, time.Now)
	views, _, err := svc.List(ctx, org, alerts.Filter{}, "", 10)
	if err != nil || len(views) != 1 {
		t.Fatalf("List: %v / %d", err, len(views))
	}
	if views[0].Detail != "timeout" {
		t.Errorf("List detail = %q, want timeout", views[0].Detail)
	}
	got, err := svc.Get(ctx, org, views[0].ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Detail != "timeout" {
		t.Errorf("Get detail = %q, want timeout", got.Detail)
	}
	var opened *alerts.Event
	for _, e := range got.Timeline {
		if e.Event != nil && e.Event.Kind == string(alerts.EventOpened) {
			opened = e.Event
		}
	}
	if opened == nil || opened.Detail != "timeout" {
		t.Errorf("timeline opened event = %+v, want detail timeout", opened)
	}
}

// sqlSource runs a statement that fails on the transaction it is handed,
// the way a broken lookup query would. Without the savepoint the
// evaluator's transaction would be aborted: every later statement fails
// with "current transaction is aborted" and the host's whole evaluation
// rolls back. With swallow it ignores the error and reports success — the
// RELEASE is then what fails, and the savepoint must still be rolled back.
type sqlSource struct{ swallow bool }

func (s sqlSource) Lookup() alerts.DetailLookup { return s }

func (s sqlSource) Detail(ctx context.Context, tx pgx.Tx, _ uuid.UUID, _ alerts.Subject) (string, error) {
	_, err := tx.Exec(ctx, `SELECT 1 / 0`)
	if err == nil {
		return "", errors.New("SELECT 1 / 0 succeeded")
	}
	if s.swallow {
		return "timeout", nil
	}
	return "", err
}

func TestAFailingLookupLeavesTheTransactionUsable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     sqlSource
		wantLog string
	}{
		{"the lookup reports the error", sqlSource{}, "division by zero"},
		{"the lookup swallows the error", sqlSource{swallow: true}, "release savepoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testdb.Open(t)
			org, host := seedHost(t, d)
			st := alerts.NewSQLStore(d)
			ctx := context.Background()
			var log bytes.Buffer
			svc := alerts.NewService(st, time.Now)
			svc.SetDetailSource(tc.src, slog.New(slog.NewTextHandler(&log, nil)))

			tx := beginTx(t, d)
			at := time.Now().UTC().Truncate(time.Microsecond)
			in := rules.AlertInput{
				OrgID: org, HostID: host, At: at,
				States: []rules.TupleState{{Tuple: rules.Tuple{CheckKey: "web", Metric: "http.up"}, State: rules.StateCrit, Since: at, EvaluatedAt: at}},
				Host:   rules.HostState{HostID: host, State: rules.StateCrit, Since: at, EvaluatedAt: at},
			}
			if err := svc.Apply(ctx, tx, in); err != nil {
				t.Fatalf("Apply: %v — a failing lookup must not fail the evaluation", err)
			}
			// The transaction is still usable: a statement after Apply
			// runs, and the commit keeps the alert.
			if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
				t.Fatalf("statement after Apply: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit: %v", err)
			}
			views, _, err := svc.List(ctx, org, alerts.Filter{}, "", 10)
			if err != nil || len(views) != 1 {
				t.Fatalf("List: %v / %d alerts, want the opened one", err, len(views))
			}
			if views[0].Detail != "" {
				t.Errorf("detail = %q, want none", views[0].Detail)
			}
			if !strings.Contains(log.String(), tc.wantLog) {
				t.Errorf("log = %q, want %q", log.String(), tc.wantLog)
			}
		})
	}
}

// A severity change whose capture failed keeps the alert's detail and
// writes none on its event; one that found nothing clears it.
func TestExecuteKeepsTheDetailWhenTheCaptureFailed(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	tx := beginTx(t, d)
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: tuple(host, "web", "", "http.up"), At: at, Severity: rules.StateWarn, Detail: "timeout"},
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d", err, len(open))
	}
	id := open[0].ID

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: at.Add(time.Minute), Severity: rules.StateCrit, KeepDetail: true},
	}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if got := alertDetail(t, tx, id); got != "timeout" {
		t.Errorf("alert detail after a failed capture = %q, want the previous timeout", got)
	}
	if got := eventDetails(t, tx, id)["severity_raised"]; got != "<nil>" {
		t.Errorf("severity_raised detail after a failed capture = %q, want none", got)
	}

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: at.Add(2 * time.Minute), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("fall: %v", err)
	}
	if got := alertDetail(t, tx, id); got != "<nil>" {
		t.Errorf("alert detail after a capture that found nothing = %q, want NULL", got)
	}
}
