// SQLStore's metric forget and metric expiry (backlog 3.88, spec
// docs/superpowers/specs/2026-11-18-vanished-metrics-and-assertion-actions-design.md
// item 5) against the real database: what forgetting one metric deletes
// and keeps, the selection's rule at its edges, the SKIP LOCKED race with
// ingest and the wait for a running evaluation — the same lock order as
// the item forget (store_sql_forget_test.go), whose helpers these reuse —
// and one expiry round forgetting both an item and a metric.
package checks_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// metricRows counts host's rows of (key, item, metric) in table.
func metricRows(t *testing.T, d *db.DB, table string, host uuid.UUID, key, item, metric string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+
		` WHERE host_id = $1 AND check_key = $2 AND item = $3 AND metric = $4`, host, key, item, metric).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// metricRuleRows is a metric's check_states plus check_state_changes rows.
func metricRuleRows(t *testing.T, d *db.DB, host uuid.UUID, key, item, metric string) int {
	t.Helper()
	return metricRows(t, d, "check_states", host, key, item, metric) +
		metricRows(t, d, "check_state_changes", host, key, item, metric)
}

// expiredMetricSet runs the store's selection and returns it as a set.
func expiredMetricSet(t *testing.T, d *db.DB, period time.Duration, limit int) map[checks.MetricRef]bool {
	t.Helper()
	refs, err := checks.NewSQLStore(d).ExpiredMetrics(ctx, period, limit)
	if err != nil {
		t.Fatal(err)
	}
	set := map[checks.MetricRef]bool{}
	for _, r := range refs {
		set[r] = true
	}
	return set
}

// TestForgetMetricRemovesOneMetricOnly: the forgotten metric loses its
// current value, rule state and state-change history, its open alert is
// closed as forgotten, its samples stay; the item's other metrics, the
// item's discovery row (check_items), the same metric of another item and
// the same tuple on another host are untouched; the host is marked for
// re-evaluation.
func TestForgetMetricRemovesOneMetricOnly(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-metric")
	host := seedHost(t, d, org, "forget-metric-host")
	other := seedHost(t, d, org, "forget-metric-other")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	scoped := db.WithOrgScope(ctx, org)

	if _, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "docker", Item: "web", Metric: "running", At: now, Value: f64(1)},
		{HostID: host, CheckKey: "docker", Item: "web", Metric: "restarts", At: now, Value: f64(0)},
		{HostID: host, CheckKey: "docker", Item: "db", Metric: "restarts", At: now, Value: f64(0)},
		{HostID: other, CheckKey: "docker", Item: "web", Metric: "restarts", At: now, Value: f64(0)},
	}); err != nil {
		t.Fatal(err)
	}
	q := gen.New(d.Pool())
	for _, tu := range [][3]string{{"docker", "web", "running"}, {"docker", "web", "restarts"}, {"docker", "db", "restarts"}} {
		putRuleState(t, q, org, host, tu[0], tu[1], tu[2])
	}
	putRuleState(t, q, org, other, "docker", "web", "restarts")
	loadHost(t, d, org, host)
	if _, ok := firstSeen(t, d, host, "docker", "web"); !ok {
		t.Fatal("setup: web was not discovered")
	}
	forgotten := openAlertCommitted(t, d, org, host, "docker", "web", "restarts")
	sibling := openAlertCommitted(t, d, org, host, "docker", "web", "running")
	otherItem := openAlertCommitted(t, d, org, host, "docker", "db", "restarts")
	dirtyBefore, _ := cursorDirtyAt(t, d, host)

	if err := svc.ForgetMetric(scoped, org, host, "docker", "web", "restarts"); err != nil {
		t.Fatalf("forget metric: %v", err)
	}

	if n := metricRows(t, d, "check_current", host, "docker", "web", "restarts"); n != 0 {
		t.Errorf("forgotten metric keeps %d current rows", n)
	}
	if n := metricRuleRows(t, d, host, "docker", "web", "restarts"); n != 0 {
		t.Errorf("forgotten metric keeps %d rule rows (states and changes)", n)
	}
	if n := metricRows(t, d, "check_samples", host, "docker", "web", "restarts"); n != 1 {
		t.Errorf("forgotten metric has %d samples, want its one kept", n)
	}
	for _, tu := range []struct {
		host               uuid.UUID
		item, metric, name string
	}{{host, "web", "running", "sibling metric"}, {host, "db", "restarts", "same metric of another item"}, {other, "web", "restarts", "same tuple on another host"}} {
		if n := metricRows(t, d, "check_current", tu.host, "docker", tu.item, tu.metric); n != 1 {
			t.Errorf("%s has %d current rows, want 1", tu.name, n)
		}
		if n := metricRuleRows(t, d, tu.host, "docker", tu.item, tu.metric); n != 2 {
			t.Errorf("%s has %d rule rows, want 2", tu.name, n)
		}
	}
	if _, ok := firstSeen(t, d, host, "docker", "web"); !ok {
		t.Error("the item lost its check_items row; a metric forget keeps the item's discovery")
	}
	if reason, events := alertClose(t, d, forgotten); reason != string(alerts.Forgotten) || events != 1 {
		t.Errorf("forgotten metric's alert: reason %q with %d closed events, want forgotten with 1", reason, events)
	}
	for name, id := range map[string]uuid.UUID{"sibling metric": sibling, "other item": otherItem} {
		if reason, _ := alertClose(t, d, id); reason != "" {
			t.Errorf("%s alert closed as %q, want it open", name, reason)
		}
	}
	if after, _ := cursorDirtyAt(t, d, host); !after.After(dirtyBefore) {
		t.Errorf("dirty_at = %v, want it moved past %v so the roll-up is re-evaluated", after, dirtyBefore)
	}
}

// TestForgetMetricNotFound: a metric with no current row, a metric of
// another organization's host and a second forget are all ErrNotFound.
func TestForgetMetricNotFound(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-metric-404")
	orgB := seedOrg(t, d, "checks-forget-metric-404-b")
	host := seedHost(t, d, org, "forget-metric-404-host")
	now := time.Now().UTC().Truncate(time.Second)
	putCurrent(t, d, org, host, "http", "", "http.dns_ms", now)
	putCurrent(t, d, org, host, "http", "", "http.up", now)
	svc := newForgetService(d, now)

	if err := svc.ForgetMetric(db.WithOrgScope(ctx, org), org, host, "http", "", "http.tls_ms"); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("unknown metric = %v, want ErrNotFound", err)
	}
	if err := svc.ForgetMetric(db.WithOrgScope(ctx, orgB), orgB, host, "http", "", "http.dns_ms"); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("another organization's metric = %v, want ErrNotFound", err)
	}
	if n := metricRows(t, d, "check_current", host, "http", "", "http.dns_ms"); n != 1 {
		t.Fatalf("a refused forget removed the row: %d left", n)
	}
	if err := svc.ForgetMetric(db.WithOrgScope(ctx, org), org, host, "http", "", "http.dns_ms"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgetMetric(db.WithOrgScope(ctx, org), org, host, "http", "", "http.dns_ms"); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("second forget = %v, want ErrNotFound", err)
	}
	if n := metricRows(t, d, "check_current", host, "http", "", "http.up"); n != 1 {
		t.Fatalf("the sibling lost its row: %d left", n)
	}
}

// TestForgetMetricWaitsForARunningEvaluation: like the item forget, the
// metric forget takes the evaluator's per-host lock first and waits for it,
// then removes what that evaluation wrote for the metric and closes its
// alert exactly once.
func TestForgetMetricWaitsForARunningEvaluation(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-metric-eval")
	host := seedHost(t, d, org, "forget-metric-eval-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	putCurrent(t, d, org, host, "http", "", "http.dns_ms", now.Add(-time.Hour))
	putCurrent(t, d, org, host, "http", "", "http.up", now)

	eval, pid := beginHolder(t, d)
	if _, err := gen.New(eval).LockOneEvalCursor(ctx, host); err != nil {
		t.Fatalf("the evaluator's claim: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.ForgetMetric(context.Background(), org, host, "http", "", "http.dns_ms") }()
	waitBlockedBy(t, d, pid)

	putRuleState(t, gen.New(eval), org, host, "http", "", "http.dns_ms")
	alertID := openAlert(t, d, eval, org, host, "http", "", "http.dns_ms")
	if err := eval.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n := metricRuleRows(t, d, host, "http", "", "http.dns_ms"); n != 0 {
		t.Errorf("the evaluation's rule rows survived the forget: %d", n)
	}
	if reason, events := alertClose(t, d, alertID); reason != string(alerts.Forgotten) || events != 1 {
		t.Errorf("the evaluation's alert: reason %q with %d closed events, want forgotten with exactly 1", reason, events)
	}
}

// TestIngestRacingTheMetricForgetLandsAfter is the item forget's SKIP
// LOCKED race (TestIngestRacingTheForgetLandsAfter) for one metric: an
// ingest has updated the metric's row, holding its row lock, but has not
// reached MarkHostDirty when the forget takes the cursor; it then waits for
// the forget on the cursor. The forget must not wait for that row in turn
// (a deadlock): it skips it, finds nothing to delete and declines with
// nothing changed, and the ingest's reading lands after it with the
// metric's rule state intact.
func TestIngestRacingTheMetricForgetLandsAfter(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-metric-ingest")
	host := seedHost(t, d, org, "forget-metric-ingest-host")
	now := time.Now().UTC().Truncate(time.Second)
	putCurrent(t, d, org, host, "http", "", "http.dns_ms", now.Add(-time.Minute))
	putCurrent(t, d, org, host, "http", "", "http.up", now.Add(-time.Minute))
	putRuleState(t, gen.New(d.Pool()), org, host, "http", "", "http.dns_ms")
	st := checks.NewSQLStore(d)

	ingest, pid := beginHolder(t, d)
	q := gen.New(ingest)
	if _, err := q.UpdateCheckCurrent(ctx, gen.UpdateCheckCurrentParams{
		OrgID: org, HostID: host, CheckKey: "http", Item: "", Metric: "http.dns_ms", Value: f64(12), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	ingestDone := make(chan error, 1)
	within := false

	// Bounded: a forget that waited for the ingest's row would wait for
	// ever (the ingest waits on the forget's cursor), so the bound turns
	// that into a failure.
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	forgot, err := st.ForgetMetric(bounded, checks.MetricRef{OrgID: org, HostID: host, CheckKey: "http", Item: "", Metric: "http.dns_ms"},
		checks.MetricForgetSteps{
			Proceed: func(checks.MetricNewest) bool {
				// The forget holds the cursor now; the ingest reaches its
				// MarkHostDirty and has to wait for the forget to end.
				go func() {
					if err := q.MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
						ingestDone <- err
						return
					}
					ingestDone <- ingest.Commit(ctx)
				}()
				waitBlockedByAny(t, d, pid)
				return true
			},
			Within: func(pgx.Tx) error { within = true; return nil },
		})
	if err != nil || forgot {
		t.Fatalf("forget = %v, %v; want false, nil (the only row was the ingest's)", forgot, err)
	}
	if within {
		t.Fatal("Within ran for a forget that deleted nothing")
	}
	if err := <-ingestDone; err != nil {
		t.Fatalf("the ingest racing the forget failed: %v", err)
	}
	var at time.Time
	if err := d.Pool().QueryRow(ctx, `SELECT at FROM check_current WHERE host_id = $1 AND check_key = 'http' AND item = '' AND metric = 'http.dns_ms'`,
		host).Scan(&at); err != nil {
		t.Fatalf("the ingest's reading: %v", err)
	}
	if !at.Equal(now) {
		t.Fatalf("metric's reading at %v, want the ingest's %v", at, now)
	}
	if n := metricRuleRows(t, d, host, "http", "", "http.dns_ms"); n != 2 {
		t.Fatalf("a declined forget removed rule rows: %d left, want 2", n)
	}
}

// TestForgetMetricProceedSeesTheReadingsAndCanDecline: the expiry's
// re-check runs inside the forget transaction with the metric's own
// reading and its item's newest, and a decline leaves everything as it
// was.
func TestForgetMetricProceedSeesTheReadingsAndCanDecline(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-metric-proceed")
	host := seedHost(t, d, org, "forget-metric-proceed-host")
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	putCurrent(t, d, org, host, "http", "", "http.dns_ms", old)
	putCurrent(t, d, org, host, "http", "", "http.up", now)
	putCurrent(t, d, org, host, "cpu", "", "usage", now.Add(time.Minute)) // another check: no say
	dirtyBefore, _ := cursorDirtyAt(t, d, host)

	var seen checks.MetricNewest
	within := false
	forgot, err := checks.NewSQLStore(d).ForgetMetric(ctx,
		checks.MetricRef{OrgID: org, HostID: host, CheckKey: "http", Item: "", Metric: "http.dns_ms"},
		checks.MetricForgetSteps{
			Proceed: func(n checks.MetricNewest) bool { seen = n; return false },
			Within:  func(pgx.Tx) error { within = true; return nil },
		})
	if err != nil || forgot {
		t.Fatalf("declined forget = %v, %v, want false, nil", forgot, err)
	}
	if !seen.Found || !seen.Metric.Equal(old) || !seen.Item.Equal(now) {
		t.Fatalf("Proceed saw %+v, want found, metric %v, item newest %v", seen, old, now)
	}
	if within {
		t.Fatal("Within ran for a declined forget")
	}
	if n := metricRows(t, d, "check_current", host, "http", "", "http.dns_ms"); n != 1 {
		t.Fatalf("a declined forget removed the row: %d left", n)
	}
	if after, _ := cursorDirtyAt(t, d, host); !after.Equal(dirtyBefore) {
		t.Fatal("a declined forget moved dirty_at")
	}

	// A metric with no row: Found is false.
	if _, err := checks.NewSQLStore(d).ForgetMetric(ctx,
		checks.MetricRef{OrgID: org, HostID: host, CheckKey: "http", Item: "", Metric: "http.tls_ms"},
		checks.MetricForgetSteps{Proceed: func(n checks.MetricNewest) bool { seen = n; return false }}); err != nil {
		t.Fatal(err)
	}
	if seen.Found {
		t.Fatalf("Proceed for a metric with no row saw %+v, want not found", seen)
	}
}

// TestExpiredMetricsBoundaries is the selection's rule at its edges, one
// host per case: a metric is due when its reading is older than its own
// item's newest by strictly more than the period — a dead metric of a
// live item. A wholly dead item (every metric quiet together) and a fresh
// metric are never selected, nor is the newest metric itself; another
// item's or another check's fresh reading has no say.
func TestExpiredMetricsBoundaries(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-metric-expiry-edges")
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	newest := now.Add(-time.Hour)
	sec := time.Second

	type reading struct {
		key, item, metric string
		at                time.Time
	}
	cases := []struct {
		name     string
		readings []reading
		due      map[string]bool // metric of ("http", "") -> due
	}{
		{"exactly the period behind", []reading{{"http", "", "dns", newest.Add(-period)}, {"http", "", "up", newest}},
			map[string]bool{"dns": false, "up": false}},
		{"a second more", []reading{{"http", "", "dns", newest.Add(-period - sec)}, {"http", "", "up", newest}},
			map[string]bool{"dns": true, "up": false}},
		{"wholly dead item", []reading{{"http", "", "dns", now.Add(-60 * 24 * time.Hour)}, {"http", "", "up", now.Add(-60*24*time.Hour + time.Minute)}},
			map[string]bool{"dns": false, "up": false}},
		{"fresh metrics", []reading{{"http", "", "dns", now}, {"http", "", "up", now}},
			map[string]bool{"dns": false, "up": false}},
		// Only the metric's own item counts: a fresh reading of another
		// item of the check, or of another check, proves nothing about it.
		{"another item and another check are fresh", []reading{
			{"http", "", "dns", now.Add(-30 * 24 * time.Hour)}, {"http", "api", "dns", now}, {"cpu", "", "dns", now}},
			map[string]bool{"dns": false}},
	}
	hosts := make([]uuid.UUID, len(cases))
	for i, tc := range cases {
		hosts[i] = seedHost(t, d, org, "metric-expiry-"+uuid.NewString()[:8])
		for _, r := range tc.readings {
			putCurrent(t, d, org, hosts[i], r.key, r.item, r.metric, r.at)
		}
	}

	got := expiredMetricSet(t, d, period, 100)
	for i, tc := range cases {
		for metric, want := range tc.due {
			ref := checks.MetricRef{OrgID: org, HostID: hosts[i], CheckKey: "http", Item: "", Metric: metric}
			if got[ref] != want {
				t.Errorf("%s: metric %q due = %v, want %v", tc.name, metric, got[ref], want)
			}
		}
	}
	for ref := range got {
		if ref.CheckKey != "http" || ref.Item != "" || ref.Metric != "dns" {
			t.Errorf("unexpected selection %+v", ref)
		}
	}
}

// TestExpiredMetricsHonoursTheLimitOldestFirst: three due metrics, a limit
// of two — the two longest gone come back.
func TestExpiredMetricsHonoursTheLimitOldestFirst(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-metric-expiry-limit")
	host := seedHost(t, d, org, "metric-expiry-limit-host")
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	putCurrent(t, d, org, host, "http", "", "up", now)
	for i, age := range []time.Duration{10, 30, 20} {
		putCurrent(t, d, org, host, "http", "", []string{"ten", "thirty", "twenty"}[i], now.Add(-age*24*time.Hour))
	}

	got := expiredMetricSet(t, d, period, 2)
	thirty := checks.MetricRef{OrgID: org, HostID: host, CheckKey: "http", Metric: "thirty"}
	twenty := checks.MetricRef{OrgID: org, HostID: host, CheckKey: "http", Metric: "twenty"}
	if len(got) != 2 || !got[thirty] || !got[twenty] {
		t.Fatalf("selection = %v, want thirty and twenty", got)
	}
}

// TestExpiryRoundForgetsItemsAndMetrics: one round of the hourly loop, run
// with no organization bound, forgets a quiet item of one organization and
// a dead metric of a live item of another — closing their alerts as
// forgotten — and leaves the live metrics and the metric's item alone.
func TestExpiryRoundForgetsItemsAndMetrics(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "checks-expiry-round-a")
	orgB := seedOrg(t, d, "checks-expiry-round-b")
	hostA := seedHost(t, d, orgA, "expiry-round-a")
	hostB := seedHost(t, d, orgB, "expiry-round-b")
	now := time.Now().UTC().Truncate(time.Microsecond)
	period := 2 * time.Hour
	gone := now.Add(-3 * time.Hour)
	// Org A: the docker item "old" is gone while the host keeps reporting.
	putCurrent(t, d, orgA, hostA, "docker", "old", "running", gone)
	putCurrent(t, d, orgA, hostA, "cpu", "", "usage", now)
	// Org B: http.dns_ms stopped, the check's other metrics go on.
	putCurrent(t, d, orgB, hostB, "http", "", "http.dns_ms", gone)
	putCurrent(t, d, orgB, hostB, "http", "", "http.up", now)
	itemAlert := openAlertCommitted(t, d, orgA, hostA, "docker", "old", "running")
	metricAlert := openAlertCommitted(t, d, orgB, hostB, "http", "", "http.dns_ms")
	liveAlert := openAlertCommitted(t, d, orgB, hostB, "http", "", "http.up")

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	checks.NewExpiryLoop(newForgetService(d, now), nil, period, time.Hour, log).Round(context.Background())

	if n := itemRows(t, d, "check_current", hostA, "docker", "old"); n != 0 {
		t.Errorf("the quiet item keeps %d current rows", n)
	}
	if n := metricRows(t, d, "check_current", hostB, "http", "", "http.dns_ms"); n != 0 {
		t.Errorf("the dead metric keeps %d current rows", n)
	}
	for _, c := range []struct {
		host              uuid.UUID
		key, item, metric string
	}{{hostA, "cpu", "", "usage"}, {hostB, "http", "", "http.up"}} {
		if n := metricRows(t, d, "check_current", c.host, c.key, c.item, c.metric); n != 1 {
			t.Errorf("%s/%s has %d current rows, want 1", c.key, c.metric, n)
		}
	}
	for name, id := range map[string]uuid.UUID{"item": itemAlert, "metric": metricAlert} {
		if reason, events := alertClose(t, d, id); reason != string(alerts.Forgotten) || events != 1 {
			t.Errorf("%s alert: %q with %d closed events, want forgotten with 1", name, reason, events)
		}
	}
	if reason, _ := alertClose(t, d, liveAlert); reason != "" {
		t.Errorf("the live metric's alert closed as %q", reason)
	}
	out := buf.String()
	for _, want := range []string{"level=INFO", orgA.String(), orgB.String()} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("round log lacks %q: %s", want, out)
		}
	}
}
