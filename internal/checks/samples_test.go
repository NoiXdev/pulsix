package checks_test

import (
	"context"
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
	if f.err != nil {
		return 0, 0, f.err
	}
	return len(obs), 0, nil
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
	accepted, rejected, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 2 || rejected != 0 {
		t.Fatalf("accepted=%d rejected=%d, want 2, 0", accepted, rejected)
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
	accepted, rejected, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || rejected != 0 {
		t.Fatalf("accepted=%d rejected=%d, want 1, 0 — an unknown check_key must still be stored", accepted, rejected)
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
	accepted, rejected, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 1, 1", accepted, rejected)
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
			accepted, rejected, err := svc.RecordObservations(scoped, org, obs)
			if err != nil {
				t.Fatal(err)
			}
			if tc.accept && (accepted != 1 || rejected != 0) {
				t.Fatalf("accepted=%d rejected=%d, want 1, 0", accepted, rejected)
			}
			if !tc.accept && (accepted != 0 || rejected != 1) {
				t.Fatalf("accepted=%d rejected=%d, want 0, 1", accepted, rejected)
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
	accepted, rejected, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 0 || rejected != 4 {
		t.Fatalf("accepted=%d rejected=%d, want 0, 4", accepted, rejected)
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
	accepted, rejected, err := svc.RecordObservations(scoped, org, obs)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || rejected != 0 {
		t.Fatalf("accepted=%d rejected=%d, want 1, 0", accepted, rejected)
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
	if accepted, rejected, err := svc.RecordObservations(scoped, org, first); err != nil || accepted != 2 || rejected != 0 {
		t.Fatalf("first batch: accepted=%d rejected=%d err=%v", accepted, rejected, err)
	}

	// /data has since been unmounted: the second batch never mentions it
	// again.
	secondAt := time.Now()
	second := []checks.Observation{
		{HostID: host, CheckKey: "disk", Item: "/", Metric: "used_percent", At: secondAt, Value: f64(11)},
	}
	if accepted, rejected, err := svc.RecordObservations(scoped, org, second); err != nil || accepted != 1 || rejected != 0 {
		t.Fatalf("second batch: accepted=%d rejected=%d err=%v", accepted, rejected, err)
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
	if accepted, rejected, err := svc.RecordObservations(scoped, org, obs); err != nil || accepted != 4 || rejected != 0 {
		t.Fatalf("accepted=%d rejected=%d err=%v", accepted, rejected, err)
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
	if _, _, err := svc.RecordObservations(db.WithOrgScope(context.Background(), orgA), orgA, obsA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RecordObservations(db.WithOrgScope(context.Background(), orgB), orgB, obsB); err != nil {
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
