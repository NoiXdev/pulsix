package checks_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

// fixedClock returns a checks.WithClock option that always answers t —
// the plausibility-window boundary tests need "now" pinned exactly, not
// merely close to real time.Now.
func fixedClock(t time.Time) checks.Option {
	return checks.WithClock(func() time.Time { return t })
}

// fakeStore's samples-related methods: this task's own tests all run
// against the real SQLStore (samples_test.go's own convention — the
// host-existence rejection rule needs a real hosts table to check against,
// so a fake would just be a second, drift-prone implementation of the
// same rule). These three exist purely so *fakeStore — shared across
// groups_test.go, definitions_test.go and resolver_test.go — keeps
// satisfying checks.Store; no test exercises them.
func (f *fakeStore) RecordObservations(_ context.Context, _ uuid.UUID, obs []checks.Observation) (accepted, rejected int, err error) {
	f.mu.Lock()
	f.recordCalls++
	f.mu.Unlock()
	if f.err != nil {
		return 0, 0, f.err
	}
	return len(obs), 0, nil
}

// reachedStore reports how many batches actually got past the service's
// own validation.
func (f *fakeStore) reachedStore() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recordCalls
}

func (f *fakeStore) CurrentForHost(_ context.Context, _, _ uuid.UUID) ([]checks.Current, error) {
	if f.err != nil {
		return nil, f.err
	}
	return nil, nil
}

func (f *fakeStore) SeriesForHost(_ context.Context, _, _ uuid.UUID, _, _, _ string, _, _ time.Time, _ int) (checks.Series, error) {
	if f.err != nil {
		return checks.Series{}, f.err
	}
	return checks.Series{}, nil
}

// TestClockOffsetMeasuredAtIngest: the manager holds an agent's own
// timestamps next to its own clock exactly once — when a batch arrives —
// and nothing persists that comparison. Without it the host page cannot
// tell a host whose clock runs four minutes behind (every reading
// accepted, stored, and permanently badged stale) from a host that
// stopped reporting, and the two remedies have nothing in common.
//
// Runs against the fake store: the measurement is the service's own, made
// before the store is reached at all.
func TestClockOffsetMeasuredAtIngest(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	host := uuid.Must(uuid.NewV7())
	svc := checks.NewService(newFakeStore(), fixedClock(now))
	org := uuid.Must(uuid.NewV7())

	if _, ok := svc.ClockOffset(host); ok {
		t.Fatal("a host that has never reported must have no measurement, not a zero one")
	}

	// A host four minutes behind, sending two readings collected a minute
	// apart. The NEWEST is what the offset is taken from: the older one is
	// older for a reason that is not the clock.
	if _, err := svc.RecordObservations(context.Background(), org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "cpu.used_pct", At: now.Add(-5 * time.Minute), Value: f64(1)},
		{HostID: host, CheckKey: "cpu", Metric: "cpu.used_pct", At: now.Add(-4 * time.Minute), Value: f64(2)},
	}); err != nil {
		t.Fatal(err)
	}
	m, ok := svc.ClockOffset(host)
	if !ok || m.Offset != 4*time.Minute {
		t.Fatalf("offset = %v (present %v), want 4m behind — taken from the newest reading in the batch", m.Offset, ok)
	}
	if !m.At.Equal(now) {
		t.Errorf("measured at %v, want the ingest moment %v", m.At, now)
	}

	// A punctual host on the same manager is not tarred with it.
	other := uuid.Must(uuid.NewV7())
	if _, err := svc.RecordObservations(context.Background(), org, []checks.Observation{
		{HostID: other, CheckKey: "cpu", Metric: "cpu.used_pct", At: now, Value: f64(3)},
	}); err != nil {
		t.Fatal(err)
	}
	if m, ok := svc.ClockOffset(other); !ok || m.Offset != 0 {
		t.Fatalf("second host offset = %v (present %v), want 0", m.Offset, ok)
	}
	if m, _ := svc.ClockOffset(host); m.Offset != 4*time.Minute {
		t.Fatalf("first host offset = %v after another host reported, want it untouched", m.Offset)
	}
}

// TestClockOffsetRejectedObservationsDoNotMeasureAnything: an observation
// outside the plausibility window is not evidence about the clock in the
// sense this measurement means — it was not stored, so no reading of it
// is on the page to explain. Measuring from it would report an hour of
// skew for a host whose real readings are punctual.
func TestClockOffsetRejectedObservationsDoNotMeasureAnything(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	host := uuid.Must(uuid.NewV7())
	svc := checks.NewService(newFakeStore(), fixedClock(now))

	if _, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()), []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "cpu.used_pct", At: now.Add(-2 * time.Hour), Value: f64(1)},
	}); err != nil {
		t.Fatal(err)
	}
	if m, ok := svc.ClockOffset(host); ok {
		t.Fatalf("offset = %v, want no measurement at all from a batch nothing survived", m)
	}
}

// TestRecordObservationsRoundTrip stores one numeric and one discrete
// Observation, then reads both back through CurrentForHost and the
// numeric one through SeriesForHost — the two read paths samples.go
// exists to serve.
func TestRecordObservationsRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-roundtrip")
	host := seedHost(t, d, org, "roundtrip-host")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))
	scoped := db.WithOrgScope(context.Background(), org)

	now := time.Now().UTC().Truncate(time.Second)
	obs := []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(42.5)},
		{HostID: host, CheckKey: "service", Item: "sshd", Metric: "state", At: now, Label: str("active")},
	}
	res, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || res.Rejected() != 0 {
		t.Fatalf("ingest = %+v, want 2 accepted and nothing rejected", res)
	}

	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 {
		t.Fatalf("current = %+v, want 2 rows", current)
	}
	byKey := map[string]checks.Current{}
	for _, c := range current {
		byKey[c.CheckKey+"/"+c.Item+"/"+c.Metric] = c
	}
	cpu, ok := byKey["cpu//usage"]
	if !ok || cpu.Value == nil || *cpu.Value != 42.5 || cpu.Label != nil || cpu.Err != "" {
		t.Fatalf("cpu current = %+v", cpu)
	}
	svcState, ok := byKey["service/sshd/state"]
	if !ok || svcState.Label == nil || *svcState.Label != "active" || svcState.Value != nil || svcState.Err != "" {
		t.Fatalf("service current = %+v", svcState)
	}

	series, err := svc.SeriesForHost(scoped, org, host, "cpu", "", "usage", now.Add(-time.Minute), now.Add(time.Minute), 4)
	if err != nil {
		t.Fatal(err)
	}
	if series.Metric != "usage" || len(series.Points) != 1 || series.Points[0].Value != 42.5 {
		t.Fatalf("series = %+v, want one bucket with value 42.5", series)
	}
}

// TestRecordObservationsUnknownCheckKeyStored is Review Focus 4: an
// Observation whose CheckKey matches no check_definitions row (here,
// none was ever created for this org) is still stored and counted
// accepted — history is keyed by check_key precisely so it survives a
// definition being edited or deleted, and RecordObservations must never
// consult check_definitions to decide whether to store something.
func TestRecordObservationsUnknownCheckKeyStored(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-unknown-key")
	host := seedHost(t, d, org, "unknown-key-host")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))
	scoped := db.WithOrgScope(context.Background(), org)

	obs := []checks.Observation{
		{HostID: host, CheckKey: "no-such-definition", Metric: "widgets", At: time.Now(), Value: f64(7)},
	}
	res, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || res.Rejected() != 0 {
		t.Fatalf("ingest = %+v, want 1 accepted — an unknown check_key must still be stored", res)
	}
	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil || len(current) != 1 || current[0].CheckKey != "no-such-definition" {
		t.Fatalf("current = %+v, %v", current, err)
	}
}

// TestRecordObservationsUnknownHostRejected is the other half of Review
// Focus 4: an Observation for a host that does not exist in the
// organization is rejected — but only that Observation, not the batch it
// arrived in.
func TestRecordObservationsUnknownHostRejected(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-unknown-host")
	host := seedHost(t, d, org, "known-host")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))
	scoped := db.WithOrgScope(context.Background(), org)

	unknownHost := uuid.Must(uuid.NewV7())
	now := time.Now()
	obs := []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(1)},
		{HostID: unknownHost, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(2)},
	}
	res, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	// Attributed to the host rule, not merely counted: an operator told
	// only "1 rejected" cannot tell this from a clock or a shape problem.
	if res.Accepted != 1 || res.UnknownHost != 1 || res.Rejected() != 1 {
		t.Fatalf("ingest = %+v, want 1 accepted and 1 rejected as an unknown host", res)
	}
	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil || len(current) != 1 {
		t.Fatalf("the known host's observation must still be stored: %+v, %v", current, err)
	}
}

// TestRecordObservationsPlausibilityWindowBoundaries pins both edges of
// the window at both ends, with an injected clock: 2 minutes minus a
// second and 1 hour minus a second are accepted; 2 minutes plus a second
// and 1 hour plus a second are rejected. Rejections are counted, never
// errors.
func TestRecordObservationsPlausibilityWindowBoundaries(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-window")
	host := seedHost(t, d, org, "window-host")
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(context.Background(), org)

	cases := []struct {
		name   string
		at     time.Time
		accept bool
	}{
		{"2min-minus-1s future: accepted", now.Add(2*time.Minute - time.Second), true},
		{"2min-plus-1s future: rejected", now.Add(2*time.Minute + time.Second), false},
		{"1h-minus-1s past: accepted", now.Add(-(time.Hour - time.Second)), true},
		{"1h-plus-1s past: rejected", now.Add(-(time.Hour + time.Second)), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := []checks.Observation{
				{HostID: host, CheckKey: "cpu", Item: uuid.Must(uuid.NewV7()).String(), Metric: "usage", At: tc.at, Value: f64(float64(i))},
			}
			res, err := svc.RecordObservations(scoped, org, obs)
			if err != nil {
				t.Fatal(err)
			}
			if tc.accept && (res.Accepted != 1 || res.Rejected() != 0) {
				t.Fatalf("ingest = %+v, want 1 accepted", res)
			}
			// A window rejection is a statement about the agent's CLOCK
			// and is counted as one — the remedy is NTP on that host, not
			// a look at the agent build.
			if !tc.accept && (res.Accepted != 0 || res.Implausible != 1 || res.Rejected() != 1) {
				t.Fatalf("ingest = %+v, want 1 rejected for an implausible timestamp", res)
			}
		})
	}
}

// TestRecordObservationsMalformedShapeRejected pins the service's own
// shape guard (samples.go's wellShaped): neither table's CHECK constraint
// can represent an Observation with both Value and Label set, with
// neither and no Err, or with Err set alongside a Value or a Label —
// RecordObservations rejects (counts) all four rather than attempting an
// insert the database would refuse (review finding 3 adds the two
// error-alongside-value/label cases; TestCheckSamplesConstraintRejects*/
// TestCheckCurrentConstraintRejects* below prove the database itself
// would refuse them too, bypassing this very guard).
func TestRecordObservationsMalformedShapeRejected(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-malformed")
	host := seedHost(t, d, org, "malformed-host")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))
	scoped := db.WithOrgScope(context.Background(), org)

	now := time.Now()
	obs := []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "both", At: now, Value: f64(1), Label: str("x")},
		{HostID: host, CheckKey: "cpu", Metric: "neither", At: now},
		{HostID: host, CheckKey: "cpu", Metric: "err-and-value", At: now, Err: "boom", Value: f64(1)},
		{HostID: host, CheckKey: "cpu", Metric: "err-and-label", At: now, Err: "boom", Label: str("x")},
	}
	res, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	// All four are the agent's own shape being wrong, which is a broken
	// agent build — never the clock, whatever their timestamps say.
	if res.Accepted != 0 || res.Malformed != 4 || res.Rejected() != 4 {
		t.Fatalf("ingest = %+v, want 4 rejected as malformed", res)
	}
}

// TestRecordObservationsCollectionError pins "a collection error is not a
// sample" (Sample delivery): an Observation with Err set writes no
// check_samples row (proven here via SeriesForHost returning no points
// for the same metric) but does upsert check_current with the error text
// and no value/label.
func TestRecordObservationsCollectionError(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-error")
	host := seedHost(t, d, org, "error-host")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))
	scoped := db.WithOrgScope(context.Background(), org)

	now := time.Now()
	obs := []checks.Observation{
		{HostID: host, CheckKey: "disk", Item: "/data", Metric: "used_percent", At: now, Err: "permission denied"},
	}
	res, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || res.Rejected() != 0 {
		t.Fatalf("ingest = %+v, want 1 accepted", res)
	}

	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil || len(current) != 1 {
		t.Fatalf("current = %+v, %v", current, err)
	}
	c := current[0]
	if c.Err != "permission denied" || c.Value != nil || c.Label != nil {
		t.Fatalf("current = %+v, want only Err set", c)
	}

	series, err := svc.SeriesForHost(scoped, org, host, "disk", "/data", "used_percent", now.Add(-time.Minute), now.Add(time.Minute), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 0 {
		t.Fatalf("series = %+v, want no points for a collection-error-only check", series)
	}
}

// TestCurrentSurvivesDisappearedItem is Review Focus 5: check_current
// rows are never deleted when a later batch stops mentioning an item —
// there is no "disappearance" event, the agent just stops sending. A
// disk's /data mount is reported once, then a later batch reports only
// /, and /data's Current row must still be present, unchanged.
func TestCurrentSurvivesDisappearedItem(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-disappear")
	host := seedHost(t, d, org, "disappear-host")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))
	scoped := db.WithOrgScope(context.Background(), org)

	firstAt := time.Now().Add(-30 * time.Minute)
	first := []checks.Observation{
		{HostID: host, CheckKey: "disk", Item: "/", Metric: "used_percent", At: firstAt, Value: f64(10)},
		{HostID: host, CheckKey: "disk", Item: "/data", Metric: "used_percent", At: firstAt, Value: f64(55)},
	}
	if res, err := svc.RecordObservations(scoped, org, first); err != nil || res.Accepted != 2 || res.Rejected() != 0 {
		t.Fatalf("first batch: %+v, err %v", res, err)
	}

	// /data has since been unmounted: the second batch never mentions it
	// again.
	secondAt := time.Now()
	second := []checks.Observation{
		{HostID: host, CheckKey: "disk", Item: "/", Metric: "used_percent", At: secondAt, Value: f64(11)},
	}
	if res, err := svc.RecordObservations(scoped, org, second); err != nil || res.Accepted != 1 || res.Rejected() != 0 {
		t.Fatalf("second batch: %+v, err %v", res, err)
	}

	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 {
		t.Fatalf("current = %+v, want /data's row to survive (2 rows total)", current)
	}
	var root, data *checks.Current
	for i := range current {
		switch current[i].Item {
		case "/":
			root = &current[i]
		case "/data":
			data = &current[i]
		}
	}
	if root == nil || root.Value == nil || *root.Value != 11 {
		t.Fatalf("/ current = %+v, want the refreshed value", root)
	}
	if data == nil {
		t.Fatal("/data's current row was deleted — it must survive, only its At goes stale")
	}
	if data.Value == nil || *data.Value != 55 {
		t.Fatalf("/data current = %+v, want the original stale value untouched", data)
	}
	if diff := data.At.Sub(firstAt); diff > time.Second || diff < -time.Second {
		t.Fatalf("/data's At = %v, want it to still read the first batch's timestamp (%v), proving it was never refreshed", data.At, firstAt)
	}
}

// TestSeriesForHostBucketed stores four samples spread across an hour and
// asks for two buckets, pinning that the store actually downsamples
// (time_bucket) rather than returning raw points.
func TestSeriesForHostBucketed(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-bucketed")
	host := seedHost(t, d, org, "bucketed-host")
	scoped := db.WithOrgScope(context.Background(), org)

	// Truncate(time.Hour) rather than time.Second: time_bucket aligns to
	// absolute epoch multiples of the bucket width (here 30 minutes), not
	// to `from` — an hour-aligned `from` guarantees each cluster of two
	// samples below sits well clear of a bucket boundary, so the test
	// cannot flake depending on what second it happens to run at.
	// Truncating down can push `from` (and every sample in it) more than
	// an hour before the real time.Now(), so the clock is pinned to `to`
	// itself instead of the real clock — every sample below is at most 32
	// minutes before `to`, comfortably inside the plausibility window
	// regardless of how far Truncate rounded down.
	from := time.Now().Add(-1 * time.Hour).Truncate(time.Hour)
	to := from.Add(time.Hour)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(to))
	obs := []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: from.Add(1 * time.Minute), Value: f64(10)},
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: from.Add(2 * time.Minute), Value: f64(20)},
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: from.Add(31 * time.Minute), Value: f64(40)},
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: from.Add(32 * time.Minute), Value: f64(60)},
	}
	if res, err := svc.RecordObservations(scoped, org, obs); err != nil || res.Accepted != 4 || res.Rejected() != 0 {
		t.Fatalf("ingest = %+v, err %v", res, err)
	}

	series, err := svc.SeriesForHost(scoped, org, host, "cpu", "", "usage", from, to, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 2 {
		t.Fatalf("points = %+v, want exactly 2 buckets", series.Points)
	}
	if series.Points[0].Value != 15 {
		t.Fatalf("bucket 1 = %v, want avg(10,20) = 15", series.Points[0].Value)
	}
	if series.Points[1].Value != 50 {
		t.Fatalf("bucket 2 = %v, want avg(40,60) = 50", series.Points[1].Value)
	}
}

// TestSeriesForHostRejectsBadRange pins the service's own input guards —
// no SQL is ever reached for either.
func TestSeriesForHostRejectsBadRange(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-bad-range")
	host := seedHost(t, d, org, "bad-range-host")
	svc := checks.NewService(checks.NewSQLStore(d))
	scoped := db.WithOrgScope(context.Background(), org)

	now := time.Now()
	if _, err := svc.SeriesForHost(scoped, org, host, "cpu", "", "usage", now, now, 4); err == nil {
		t.Fatal("to == from must be rejected")
	}
	if _, err := svc.SeriesForHost(scoped, org, host, "cpu", "", "usage", now, now.Add(time.Hour), 0); err == nil {
		t.Fatal("buckets < 1 must be rejected")
	}
}

// TestRecordObservationsOrgIsolation: two organizations, each with its own
// host — recording an Observation for one never becomes visible through
// the other's CurrentForHost/SeriesForHost, exactly the isolation every
// other store in this package pins.
func TestRecordObservationsOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "checks-samples-isolation-a")
	orgB := seedOrg(t, d, "checks-samples-isolation-b")
	hostA := seedHost(t, d, orgA, "isolation-host-a")
	hostB := seedHost(t, d, orgB, "isolation-host-b")
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(time.Now()))

	now := time.Now()
	obsA := []checks.Observation{{HostID: hostA, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(1)}}
	obsB := []checks.Observation{{HostID: hostB, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(2)}}
	if _, err := svc.RecordObservations(db.WithOrgScope(context.Background(), orgA), orgA, obsA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordObservations(db.WithOrgScope(context.Background(), orgB), orgB, obsB); err != nil {
		t.Fatal(err)
	}

	currentA, err := svc.CurrentForHost(db.WithOrgScope(context.Background(), orgA), orgA, hostA)
	if err != nil || len(currentA) != 1 {
		t.Fatalf("orgA current = %+v, %v", currentA, err)
	}
	currentB, err := svc.CurrentForHost(db.WithOrgScope(context.Background(), orgB), orgB, hostB)
	if err != nil || len(currentB) != 1 {
		t.Fatalf("orgB current = %+v, %v", currentB, err)
	}

	// orgA scoped to itself must not see orgB's host at all — the row-level
	// security policy admits nothing for a host id outside the bound org.
	crossCurrent, err := svc.CurrentForHost(db.WithOrgScope(context.Background(), orgA), orgA, hostB)
	if err != nil || len(crossCurrent) != 0 {
		t.Fatalf("orgA reading orgB's host = %+v, %v, want empty", crossCurrent, err)
	}
}

// TestRetentionPolicyPresentInCatalog: migration 00044 sets a 90-day
// retention policy on check_samples via add_retention_policy — this reads
// it back from TimescaleDB's own catalog after Open (which runs every
// migration), rather than merely trusting the migration file parsed.
func TestRetentionPolicyPresentInCatalog(t *testing.T) {
	url := testdb.URL(t)
	admin, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	rows, err := admin.Pool().Query(context.Background(),
		`SELECT proc_name, config->>'drop_after' FROM timescaledb_information.jobs WHERE hypertable_name = 'check_samples' AND proc_name = 'policy_retention'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var procName string
		var dropAfter *string
		if err := rows.Scan(&procName, &dropAfter); err != nil {
			t.Fatal(err)
		}
		found = true
		if dropAfter == nil {
			t.Fatalf("policy_retention job has no drop_after configured")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no policy_retention job found for check_samples — the migration's add_retention_policy call is missing or was not applied")
	}
}

// TestCheckSamplesConstraintRejectsBothValueAndLabel and
// TestCheckSamplesConstraintRejectsNeitherValueNorLabel exercise
// check_samples' own CHECK constraint in isolation, through the raw
// generated query (gen.Queries.InsertCheckSample on the bare pool — the
// same seam seedHost already uses) rather than through
// SQLStore.RecordObservations: RecordObservations always also upserts
// check_current for the same shape, and check_current's own CHECK
// happens to forbid the same "both set"/"neither set" shapes whenever
// error is unset — going through RecordObservations would make these two
// tests pass even if check_samples' own CHECK were missing entirely,
// because check_current's CHECK would still catch the insert one
// statement later. Calling InsertCheckSample directly reaches only
// check_samples, so only its own constraint is under test.
func TestCheckSamplesConstraintRejectsBothValueAndLabel(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-check-both")
	host := seedHost(t, d, org, "check-both-host")
	q := gen.New(d.Pool())

	err := q.InsertCheckSample(context.Background(), gen.InsertCheckSampleParams{
		At: time.Now(), OrgID: org, HostID: host, CheckKey: "cpu", Metric: "usage",
		Value: f64(1), Label: str("x"),
	})
	if err == nil {
		t.Fatal("check_samples must refuse a row with both value and label set")
	}
}

func TestCheckSamplesConstraintRejectsNeitherValueNorLabel(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-check-neither")
	host := seedHost(t, d, org, "check-neither-host")
	q := gen.New(d.Pool())

	err := q.InsertCheckSample(context.Background(), gen.InsertCheckSampleParams{
		At: time.Now(), OrgID: org, HostID: host, CheckKey: "cpu", Metric: "usage",
	})
	if err == nil {
		t.Fatal("check_samples must refuse a row with neither value nor label set")
	}
}

// TestCheckCurrentConstraintRejectsErrorAlongsideValue and
// TestCheckCurrentConstraintRejectsErrorAlongsideLabel exercise
// check_current's own CHECK directly through SQLStore.RecordObservations:
// an Err-set Observation makes recordHostObservations skip the
// check_samples insert entirely (Observation's own doc comment), so
// these two shapes only ever reach check_current — unlike the two tests
// above, there is no risk of check_samples' constraint masking the
// result here, since check_samples is never touched for either case.
func TestCheckCurrentConstraintRejectsErrorAlongsideValue(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-current-check-value")
	host := seedHost(t, d, org, "current-check-value-host")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	_, _, err := st.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: time.Now(), Err: "boom", Value: f64(1)},
	})
	if err == nil {
		t.Fatal("check_current must refuse a row with both error and value set")
	}
}

func TestCheckCurrentConstraintRejectsErrorAlongsideLabel(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-current-check-label")
	host := seedHost(t, d, org, "current-check-label-host")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	_, _, err := st.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: time.Now(), Err: "boom", Label: str("x")},
	})
	if err == nil {
		t.Fatal("check_current must refuse a row with both error and label set")
	}
}

// obsAt builds one well-shaped numeric Observation for the contract-bound
// tests below: only the field under test ever varies.
func obsAt(host uuid.UUID, now time.Time, key, item, metric string) checks.Observation {
	return checks.Observation{HostID: host, CheckKey: key, Item: item, Metric: metric, At: now, Value: f64(1)}
}

// TestRecordObservationsBatchSizeBoundary: the count bound is a REFUSAL of
// the whole batch (ErrInvalidInput, the shape agents.RecordFacts already
// uses for its own contract's bounds), never a silent truncation, and the
// store is not reached at all — a batch over the bound must not cost a
// pool connection or a transaction, which is the entire point of bounding
// it. Exactly at the bound is accepted: an off-by-one here would refuse a
// legitimate host.
func TestRecordObservationsBatchSizeBoundary(t *testing.T) {
	now := time.Now().UTC()
	host := uuid.Must(uuid.NewV7())
	build := func(n int) []checks.Observation {
		obs := make([]checks.Observation, n)
		for i := range obs {
			obs[i] = obsAt(host, now, "cpu", "", "cpu.used_pct")
		}
		return obs
	}

	t.Run("at the bound", func(t *testing.T) {
		store := newFakeStore()
		svc := checks.NewService(store, fixedClock(now))
		res, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()), build(checks.MaxObservationsPerBatch))
		if err != nil {
			t.Fatalf("exactly %d observations must be accepted: %v", checks.MaxObservationsPerBatch, err)
		}
		if res.Accepted != checks.MaxObservationsPerBatch || res.Rejected() != 0 {
			t.Fatalf("ingest = %+v, want %d accepted", res, checks.MaxObservationsPerBatch)
		}
		if store.reachedStore() != 1 {
			t.Fatalf("store calls = %d, want 1", store.reachedStore())
		}
	})

	t.Run("one over the bound", func(t *testing.T) {
		store := newFakeStore()
		svc := checks.NewService(store, fixedClock(now))
		res, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()), build(checks.MaxObservationsPerBatch+1))
		if !errors.Is(err, checks.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
		if res.Accepted != 0 || res.Rejected() != 0 {
			t.Fatalf("ingest = %+v, want nothing counted at all for a refused batch", res)
		}
		if store.reachedStore() != 0 {
			t.Fatalf("store calls = %d, want 0: a refused batch must never reach the store", store.reachedStore())
		}
	})
}

// TestRecordObservationsFieldLengthBoundaries: check_key, item and metric
// each carry their own byte bound, each refusing the whole batch with
// ErrInvalidInput one byte over and accepting it exactly at the bound.
// Every case puts the oversized observation SECOND, so a fix that only
// looked at obs[0] would not pass.
func TestRecordObservationsFieldLengthBoundaries(t *testing.T) {
	now := time.Now().UTC()
	host := uuid.Must(uuid.NewV7())
	cases := []struct {
		name  string
		limit int
		build func(s string) checks.Observation
	}{
		{"check_key", checks.MaxCheckKeyLen, func(s string) checks.Observation { return obsAt(host, now, s, "", "m") }},
		{"item", checks.MaxItemLen, func(s string) checks.Observation { return obsAt(host, now, "cpu", s, "m") }},
		{"metric", checks.MaxMetricLen, func(s string) checks.Observation { return obsAt(host, now, "cpu", "", s) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := checks.NewService(store, fixedClock(now))
			org := uuid.Must(uuid.NewV7())

			atLimit := []checks.Observation{obsAt(host, now, "cpu", "", "m"), tc.build(strings.Repeat("x", tc.limit))}
			if _, err := svc.RecordObservations(context.Background(), org, atLimit); err != nil {
				t.Fatalf("%s at exactly %d bytes must be accepted: %v", tc.name, tc.limit, err)
			}
			if store.reachedStore() != 1 {
				t.Fatalf("store calls = %d, want 1", store.reachedStore())
			}

			over := []checks.Observation{obsAt(host, now, "cpu", "", "m"), tc.build(strings.Repeat("x", tc.limit+1))}
			_, err := svc.RecordObservations(context.Background(), org, over)
			if !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("%s at %d bytes: err = %v, want ErrInvalidInput", tc.name, tc.limit+1, err)
			}
			if store.reachedStore() != 1 {
				t.Fatalf("store calls = %d, want no further call after the refusal", store.reachedStore())
			}
		})
	}
}

// TestRecordObservationsRejectsEmptyKeyOrMetric: an observation with no
// check_key or no metric names nothing an operator could ever read back,
// and would still occupy a permanent check_current row — refused for the
// same reason agents.RecordFacts refuses an empty fact key. An empty item
// stays legal: that is what a whole-host metric reports.
func TestRecordObservationsRejectsEmptyKeyOrMetric(t *testing.T) {
	now := time.Now().UTC()
	host := uuid.Must(uuid.NewV7())
	cases := map[string]checks.Observation{
		"empty check_key": obsAt(host, now, "", "", "cpu.used_pct"),
		"empty metric":    obsAt(host, now, "cpu", "", ""),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			svc := checks.NewService(store, fixedClock(now))
			_, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()),
				[]checks.Observation{obsAt(host, now, "cpu", "", "m"), bad})
			if !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if store.reachedStore() != 0 {
				t.Fatalf("store calls = %d, want 0", store.reachedStore())
			}
		})
	}

	t.Run("empty item accepted", func(t *testing.T) {
		store := newFakeStore()
		svc := checks.NewService(store, fixedClock(now))
		if _, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()),
			[]checks.Observation{obsAt(host, now, "cpu", "", "cpu.used_pct")}); err != nil {
			t.Fatalf("a whole-host metric (empty item) must stay legal: %v", err)
		}
	})
}

// TestRecordObservationsAtMaxFieldLengthsFitsTheLookupIndex is the reason
// the length bounds exist in the numbers they do: check_samples_lookup_idx
// is a btree over (org_id, host_id, check_key, item, metric, at), and a
// btree index row may not exceed 8191 bytes. A batch at EVERY bound at
// once — the largest row the contract can now produce — must still insert
// against the real database, or the bounds would merely have moved the
// "index row requires N bytes" failure rather than removed it.
func TestRecordObservationsAtMaxFieldLengthsFitsTheLookupIndex(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-maxlen")
	host := seedHost(t, d, org, "maxlen-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(context.Background(), org)

	key := incompressible(t, checks.MaxCheckKeyLen)
	item := incompressible(t, checks.MaxItemLen)
	metric := incompressible(t, checks.MaxMetricLen)
	res, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: key, Item: item, Metric: metric, At: now, Value: f64(1)},
	})
	if err != nil {
		t.Fatalf("a row at every length bound must fit the lookup index: %v", err)
	}
	if res.Accepted != 1 || res.Rejected() != 0 {
		t.Fatalf("ingest = %+v, want 1 accepted", res)
	}

	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0].CheckKey != key || current[0].Item != item || current[0].Metric != metric {
		t.Fatalf("current = %+v, want the maximal-length tuple stored verbatim", current)
	}
}

// incompressible returns n bytes of random base64 — deliberately NOT a
// repeated character. PostgreSQL compresses a btree index entry's varlena
// values, so strings.Repeat("x", n) fits an index row at sizes real data
// never would: probed against the container, 3×3000 repeated bytes insert
// happily while 3×3000 random ones fail with "index row requires 9064
// bytes, maximum size is 8191". A length bound is only honestly tested
// against data that cannot compress away.
func incompressible(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)[:n]
}

// TestLookupIndexRejectsWhatTheContractBoundsForbid is the other half of
// TestRecordObservationsAtMaxFieldLengthsFitsTheLookupIndex: it goes
// straight to the store, bypassing the service's bounds, and shows that
// the strings they forbid really are the ones check_samples_lookup_idx
// cannot hold. Without the bounds this is what the ingest path did with
// an agent-chosen key — a store error, not a counted rejection, so the
// whole batch rolled back and the stream died with Internal. If
// PostgreSQL ever raised its 8191-byte index-row limit this test would
// start failing, which is the right moment to revisit the numbers rather
// than to discover them by accident.
func TestLookupIndexRejectsWhatTheContractBoundsForbid(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-samples-index-limit")
	host := seedHost(t, d, org, "index-limit-host")
	store := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	big := incompressible(t, 3000)
	_, _, err := store.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: big, Item: big, Metric: big, At: time.Now(), Value: f64(1)},
	})
	if err == nil {
		t.Fatal("a 3x3000-byte key/item/metric must not fit the lookup index; the contract bounds exist to keep it off this path")
	}
	if !strings.Contains(err.Error(), "index row requires") {
		t.Fatalf("err = %v, want the btree index-row size failure", err)
	}
}

// TestRecordObservationsShapeIsJudgedBeforeTheClock pins the order of the
// two rules, which is argued for in RecordObservations itself ("counting
// it as a clock problem would send an operator to NTP for an agent bug")
// and was held by nothing: an observation is normally either malformed or
// implausible, so swapping the switch arms passed every test. The input
// that separates them is one that is BOTH — a broken shape carrying a
// timestamp outside the window — and until now no test sent one.
func TestRecordObservationsShapeIsJudgedBeforeTheClock(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	host := uuid.Must(uuid.NewV7())
	svc := checks.NewService(newFakeStore(), fixedClock(now))

	// Value AND label set (impossible for either table's CHECK), and two
	// hours old (maxPast is one). Both rules would fire; only one gets to
	// name the cause, and the name decides where an operator looks.
	res, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()), []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "cpu.used_pct", At: now.Add(-2 * time.Hour),
			Value: f64(1), Label: str("active")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Malformed != 1 || res.Implausible != 0 {
		t.Fatalf("malformed=%d implausible=%d, want 1 and 0: a broken shape is an agent bug, "+
			"and calling it a clock problem sends the operator to NTP for it", res.Malformed, res.Implausible)
	}
	if res.Accepted != 0 || res.Rejected() != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 0 and 1", res.Accepted, res.Rejected())
	}

	// The control: the same timestamp on a WELL-shaped observation is the
	// clock, so the ordering is not simply "always blame the shape".
	res, err = svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()), []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "cpu.used_pct", At: now.Add(-2 * time.Hour), Value: f64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Implausible != 1 || res.Malformed != 0 {
		t.Fatalf("implausible=%d malformed=%d, want 1 and 0", res.Implausible, res.Malformed)
	}
}

// TestClockOffsetMeasuresOnlyWhatSurvivedTheBatch: the offset is taken
// from the newest observation the batch actually STORED, not the newest it
// arrived with. TestClockOffsetRejectedObservationsDoNotMeasureAnything
// looks like it covers this and does not — its batch is rejected whole, so
// it returns at the "nothing survived" guard before the measurement is
// reached at all, and measuring from the raw batch instead passed it.
//
// The distinguishing input is a MIXED batch: one punctual reading that is
// stored, alongside one far-future reading that is not. Measuring from the
// raw batch would report a healthy host as minutes ahead of the manager,
// which is the host page's most visible claim about a host.
func TestClockOffsetMeasuresOnlyWhatSurvivedTheBatch(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	host := uuid.Must(uuid.NewV7())
	svc := checks.NewService(newFakeStore(), fixedClock(now))

	res, err := svc.RecordObservations(context.Background(), uuid.Must(uuid.NewV7()), []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "cpu.used_pct", At: now, Value: f64(1)},
		// A day ahead: far outside maxFuture, so it is counted and dropped.
		{HostID: host, CheckKey: "cpu", Metric: "cpu.load1", At: now.Add(24 * time.Hour), Value: f64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || res.Implausible != 1 {
		t.Fatalf("accepted=%d implausible=%d, want 1 and 1", res.Accepted, res.Implausible)
	}
	m, ok := svc.ClockOffset(host)
	if !ok {
		t.Fatal("a batch with a stored reading must produce a measurement")
	}
	if m.Offset != 0 {
		t.Fatalf("offset = %v, want 0: the only reading that was stored was punctual, and the "+
			"one that was not is exactly the evidence this measurement may not use", m.Offset)
	}
}
