// The service half of forgetting one metric (backlog 3.88), against fakes:
// what ForgetMetric and ForgetExpiredMetrics hand the store and the alert
// closer, and the metric expiry rule the re-check inside each forget
// applies. The store's own behaviour is in store_sql_forget_metric_test.go
// against the real database.
package checks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/checks"
)

// fakeStore's metric forget methods exist so *fakeStore keeps satisfying
// checks.Store; the tests below use metricStore instead.
func (f *fakeStore) ForgetMetric(context.Context, checks.MetricRef, checks.MetricForgetSteps) (bool, error) {
	return false, f.err
}

func (f *fakeStore) ExpiredMetrics(context.Context, time.Duration, int) ([]checks.MetricRef, error) {
	return nil, f.err
}

// metricStore is forgetStore for metrics: it records what the service
// asked for and plays the store's part of MetricForgetSteps in SQLStore's
// order — Proceed with the readings it was given, then Within with a nil
// transaction.
type metricStore struct {
	*fakeStore
	expired []checks.MetricRef
	newest  map[checks.MetricRef]checks.MetricNewest
	exists  map[checks.MetricRef]bool
	fail    map[checks.MetricRef]error

	expiredCall struct {
		period time.Duration
		limit  int
	}
	forgot []checks.MetricRef
}

func newMetricStore() *metricStore {
	return &metricStore{
		fakeStore: newFakeStore(),
		newest:    map[checks.MetricRef]checks.MetricNewest{},
		exists:    map[checks.MetricRef]bool{},
		fail:      map[checks.MetricRef]error{},
	}
}

func (f *metricStore) ExpiredMetrics(_ context.Context, period time.Duration, limit int) ([]checks.MetricRef, error) {
	f.expiredCall.period, f.expiredCall.limit = period, limit
	return f.expired, nil
}

func (f *metricStore) ForgetMetric(_ context.Context, ref checks.MetricRef, steps checks.MetricForgetSteps) (bool, error) {
	if err := f.fail[ref]; err != nil {
		return false, err
	}
	if steps.Proceed != nil && !steps.Proceed(f.newest[ref]) {
		return false, nil
	}
	if !f.exists[ref] {
		return false, nil
	}
	if err := steps.Within(nil); err != nil {
		return false, err
	}
	f.forgot = append(f.forgot, ref)
	return true, nil
}

// metricCloseCall is one CloseForgottenMetric call the fake closer saw.
type metricCloseCall struct {
	ref checks.MetricRef
	now time.Time
}

func (c *fakeCloser) CloseForgottenMetric(_ context.Context, _ pgx.Tx, orgID, hostID uuid.UUID, checkKey, item, metric string, now time.Time) (int, error) {
	c.metricCalls = append(c.metricCalls, metricCloseCall{
		ref: checks.MetricRef{OrgID: orgID, HostID: hostID, CheckKey: checkKey, Item: item, Metric: metric}, now: now,
	})
	return 1, c.err
}

func TestForgetMetricClosesThatMetricsAlertsInsideTheForget(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	ref := checks.MetricRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "http", Item: "", Metric: "http.dns_ms"}
	st := newMetricStore()
	st.exists[ref] = true
	closer := &fakeCloser{}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(closer))

	if err := svc.ForgetMetric(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item, ref.Metric); err != nil {
		t.Fatalf("forget metric: %v", err)
	}
	if len(st.forgot) != 1 || st.forgot[0] != ref {
		t.Fatalf("store forgot %+v, want exactly %+v", st.forgot, ref)
	}
	if len(closer.metricCalls) != 1 || closer.metricCalls[0] != (metricCloseCall{ref: ref, now: now}) {
		t.Fatalf("closer metric calls = %+v, want one for %+v at %v", closer.metricCalls, ref, now)
	}
	if len(closer.calls) != 0 {
		t.Fatalf("the item-level close ran for a metric forget: %+v", closer.calls)
	}
}

func TestForgetMetricRefusals(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	storeErr := errors.New("connection reset")
	closeErr := errors.New("alerts unavailable")
	tests := []struct {
		name             string
		closer           checks.AlertCloser
		checkKey, metric string
		setup            func(st *metricStore, ref checks.MetricRef)
		want             error
	}{
		{name: "no current row is not found", closer: &fakeCloser{}, checkKey: "http", metric: "http.dns_ms", want: checks.ErrNotFound},
		{name: "empty check_key is invalid", closer: &fakeCloser{}, checkKey: "", metric: "http.dns_ms", want: checks.ErrInvalidInput},
		{name: "empty metric is invalid", closer: &fakeCloser{}, checkKey: "http", metric: "", want: checks.ErrInvalidInput},
		{name: "a store failure propagates", closer: &fakeCloser{}, checkKey: "http", metric: "http.dns_ms",
			setup: func(st *metricStore, ref checks.MetricRef) { st.fail[ref] = storeErr }, want: storeErr},
		{name: "an alert close failure propagates", closer: &fakeCloser{err: closeErr}, checkKey: "http", metric: "http.dns_ms",
			setup: func(st *metricStore, ref checks.MetricRef) { st.exists[ref] = true }, want: closeErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := checks.MetricRef{OrgID: org, HostID: host, CheckKey: tc.checkKey, Item: "", Metric: tc.metric}
			st := newMetricStore()
			if tc.setup != nil {
				tc.setup(st, ref)
			}
			svc := checks.NewService(st, checks.WithAlertCloser(tc.closer))
			if err := svc.ForgetMetric(ctx, org, host, tc.checkKey, "", tc.metric); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestForgetMetricRefusesTheItemsLastValue (backlog 3.88, final review):
// the manual forget reads, under the host's evaluation lock, how many
// other current rows the metric's item has; with none it refuses with
// ErrLastMetric — the item forget is the way — and nothing is deleted or
// closed. With a sibling left it forgets as before.
func TestForgetMetricRefusesTheItemsLastValue(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	ref := checks.MetricRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "docker", Item: "web", Metric: "running"}
	st := newMetricStore()
	st.exists[ref] = true
	st.newest[ref] = checks.MetricNewest{Found: true, Metric: now, Item: now, Siblings: 0}
	closer := &fakeCloser{}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(closer))

	if err := svc.ForgetMetric(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item, ref.Metric); !errors.Is(err, checks.ErrLastMetric) {
		t.Fatalf("forget the last value = %v, want ErrLastMetric", err)
	}
	if len(st.forgot) != 0 || len(closer.metricCalls) != 0 {
		t.Fatalf("a refused forget forgot %+v and closed %+v", st.forgot, closer.metricCalls)
	}

	st.newest[ref] = checks.MetricNewest{Found: true, Metric: now.Add(-time.Hour), Item: now, Siblings: 1}
	if err := svc.ForgetMetric(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item, ref.Metric); err != nil {
		t.Fatalf("forget with a sibling left: %v", err)
	}
	if len(st.forgot) != 1 {
		t.Fatalf("store forgot %+v, want the metric", st.forgot)
	}
}

// TestForgetMetricWithoutAlertCloserRefuses: like the item forget, a
// Service wired without the alert port must not forget a metric and leave
// its alert open for ever.
func TestForgetMetricWithoutAlertCloserRefuses(t *testing.T) {
	st := newMetricStore()
	ref := checks.MetricRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "http", Metric: "http.dns_ms"}
	st.exists[ref] = true
	st.expired = []checks.MetricRef{ref}
	svc := checks.NewService(st)

	if err := svc.ForgetMetric(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item, ref.Metric); err == nil {
		t.Fatal("ForgetMetric without an alert closer succeeded")
	}
	if _, err := svc.ForgetExpiredMetrics(ctx, time.Hour, 10); err == nil {
		t.Fatal("ForgetExpiredMetrics without an alert closer succeeded")
	}
	if len(st.forgot) != 0 {
		t.Fatalf("store forgot %+v without an alert closer", st.forgot)
	}
}

// TestForgetExpiredMetricsCountsPerOrganization: the selection is asked
// for the period and the limit; each selected metric is forgotten with its
// alert close at the service's now; the counts are per organization, and a
// metric the re-check finds no longer due — it reported again — is not
// counted.
func TestForgetExpiredMetricsCountsPerOrganization(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	orgA, orgB := uuid.New(), uuid.New()
	dead := checks.MetricNewest{Found: true, Metric: now.Add(-period - time.Hour), Item: now}
	a1 := checks.MetricRef{OrgID: orgA, HostID: uuid.New(), CheckKey: "http", Item: "", Metric: "http.dns_ms"}
	a2 := checks.MetricRef{OrgID: orgA, HostID: uuid.New(), CheckKey: "http", Item: "", Metric: "http.tls_ms"}
	b1 := checks.MetricRef{OrgID: orgB, HostID: uuid.New(), CheckKey: "smart", Item: "/dev/sda", Metric: "temperature"}
	back := checks.MetricRef{OrgID: orgB, HostID: uuid.New(), CheckKey: "http", Item: "", Metric: "http.body_match"}

	st := newMetricStore()
	st.expired = []checks.MetricRef{a1, a2, b1, back}
	for _, r := range st.expired {
		st.exists[r] = true
		st.newest[r] = dead
	}
	st.newest[back] = checks.MetricNewest{Found: true, Metric: now, Item: now}
	closer := &fakeCloser{}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(closer))

	counts, err := svc.ForgetExpiredMetrics(ctx, period, 50)
	if err != nil {
		t.Fatalf("forget expired metrics: %v", err)
	}
	if len(counts) != 2 || counts[orgA] != 2 || counts[orgB] != 1 {
		t.Fatalf("counts = %v, want 2 in A and 1 in B", counts)
	}
	if c := st.expiredCall; c.period != period || c.limit != 50 {
		t.Fatalf("selection asked for %+v, want period %v, limit 50", c, period)
	}
	if len(closer.metricCalls) != 3 {
		t.Fatalf("closer called %d times, want 3 (not for the metric that came back)", len(closer.metricCalls))
	}
	for _, c := range closer.metricCalls {
		if !c.now.Equal(now) || c.ref == back {
			t.Fatalf("closer call %+v, want now %v and never the metric that came back", c, now)
		}
	}
}

// TestForgetExpiredMetricsKeepsGoingPastAFailure: one broken metric must
// not starve the rest.
func TestForgetExpiredMetricsKeepsGoingPastAFailure(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	org := uuid.New()
	broken := checks.MetricRef{OrgID: org, HostID: uuid.New(), CheckKey: "http", Metric: "broken"}
	fine := checks.MetricRef{OrgID: org, HostID: uuid.New(), CheckKey: "http", Metric: "fine"}
	boom := errors.New("boom")
	st := newMetricStore()
	st.expired = []checks.MetricRef{broken, fine}
	st.fail[broken] = boom
	st.exists[fine] = true
	st.newest[fine] = checks.MetricNewest{Found: true, Metric: now.Add(-200 * time.Hour), Item: now}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(&fakeCloser{}))

	counts, err := svc.ForgetExpiredMetrics(ctx, 168*time.Hour, 10)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the failure", err)
	}
	if counts[org] != 1 {
		t.Fatalf("counts = %v, want the metric behind the failure forgotten", counts)
	}
}

func TestForgetExpiredMetricsRefusesNonPositiveArguments(t *testing.T) {
	svc := checks.NewService(newMetricStore(), checks.WithAlertCloser(&fakeCloser{}))
	for _, tc := range []struct {
		period time.Duration
		limit  int
	}{{0, 10}, {-time.Hour, 10}, {time.Hour, 0}, {time.Hour, -1}} {
		if _, err := svc.ForgetExpiredMetrics(ctx, tc.period, tc.limit); !errors.Is(err, checks.ErrInvalidInput) {
			t.Errorf("ForgetExpiredMetrics(%v, %d) = %v, want ErrInvalidInput", tc.period, tc.limit, err)
		}
	}
}

// TestForgetExpiredMetricsRecheckBoundaries is the metric expiry rule the
// re-check inside each forget applies: the metric's reading is older than
// its item's newest by strictly more than the period. The same boundaries
// TestExpiredMetricsBoundaries pins on the store's selection.
func TestForgetExpiredMetricsRecheckBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	itemNewest := now.Add(-time.Hour)
	tests := []struct {
		name   string
		newest checks.MetricNewest
		want   bool
	}{
		{"exactly the period behind its item is not due", checks.MetricNewest{Found: true, Metric: itemNewest.Add(-period), Item: itemNewest}, false},
		{"a second more is due", checks.MetricNewest{Found: true, Metric: itemNewest.Add(-period - time.Second), Item: itemNewest}, true},
		{"the item's newest itself is never due", checks.MetricNewest{Found: true, Metric: itemNewest, Item: itemNewest}, false},
		// The whole item went quiet together long ago: nothing newer to
		// prove the metric gone — that is the item expiry's case.
		{"a wholly quiet item", checks.MetricNewest{Found: true, Metric: now.Add(-60 * 24 * time.Hour), Item: now.Add(-60 * 24 * time.Hour)}, false},
		{"no row left", checks.MetricNewest{Found: false, Metric: time.Unix(0, 0), Item: itemNewest}, false},
		// Only errors besides it (the SMART disk smartctl cannot open):
		// no error-free newest, which the store reads as 'epoch'.
		{"an item with no error-free reading", checks.MetricNewest{Found: true, Metric: now.Add(-60 * 24 * time.Hour), Item: time.Unix(0, 0), Siblings: 1}, false},
		// Held on its item's state (a sleeping disk's sector count, backlog
		// 3.9b ruling): its last reading waits for the item to wake, however
		// far behind the item's state it falls.
		{"a held metric is never due", checks.MetricNewest{Found: true, Metric: itemNewest.Add(-period - time.Second), Item: itemNewest, Held: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := checks.MetricRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "http", Metric: "http.dns_ms"}
			st := newMetricStore()
			st.expired = []checks.MetricRef{ref}
			st.exists[ref] = true
			st.newest[ref] = tc.newest
			svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(&fakeCloser{}))

			counts, err := svc.ForgetExpiredMetrics(ctx, period, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := counts[ref.OrgID] == 1; got != tc.want {
				t.Fatalf("forgotten = %v, want %v", got, tc.want)
			}
		})
	}
}
