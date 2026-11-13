package monitorsettings_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/monitorsettings"
)

// seedHost creates a bare zone and host in org (check_samples' and
// check_state_changes' composite foreign key needs a real host).
func seedHost(t *testing.T, q *gen.Queries, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: name, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: name, Facts: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return host.ID
}

// seedSamples writes n samples of host at at + i milliseconds.
func seedSamples(t *testing.T, d *db.DB, org, host uuid.UUID, at time.Time, n int) {
	t.Helper()
	if _, err := d.Exec(db.WithOrgScope(ctx, org), `
		INSERT INTO check_samples (at, org_id, host_id, check_key, item, metric, value)
		SELECT $3::timestamptz + make_interval(secs => g / 1000.0), $1, $2, 'cpu', '', 'usage', 1
		FROM generate_series(0, $4::int - 1) g`, org, host, at, n); err != nil {
		t.Fatal(err)
	}
}

// seedStateChanges writes n state changes of host at at + i milliseconds.
func seedStateChanges(t *testing.T, d *db.DB, org, host uuid.UUID, at time.Time, n int) {
	t.Helper()
	if _, err := d.Exec(db.WithOrgScope(ctx, org), `
		INSERT INTO check_state_changes (at, org_id, host_id, check_key, item, metric, from_state, to_state)
		SELECT $3::timestamptz + make_interval(secs => g / 1000.0), $1, $2, 'cpu', '', 'usage', 'ok', 'warn'
		FROM generate_series(0, $4::int - 1) g`, org, host, at, n); err != nil {
		t.Fatal(err)
	}
}

// countRows counts org's rows of table with at < before (older) and at >=
// before (newer), outside any scope.
func countRows(t *testing.T, d *db.DB, table string, org uuid.UUID, before time.Time) (older, newer int) {
	t.Helper()
	if err := d.Pool().QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE at < $2), count(*) FILTER (WHERE at >= $2)
		FROM `+table+` WHERE org_id = $1`, org, before).Scan(&older, &newer); err != nil {
		t.Fatal(err)
	}
	return older, newer
}

// countingStore wraps the SQL store and records every batch's size.
type countingStore struct {
	*monitorsettings.PurgeSQLStore
	batches map[monitorsettings.Table][]int64
}

func (c *countingStore) DeleteBatch(ctx context.Context, table monitorsettings.Table, org uuid.UUID, r monitorsettings.TimeRange, limit int) (int64, error) {
	n, err := c.PurgeSQLStore.DeleteBatch(ctx, table, org, r, limit)
	c.batches[table] = append(c.batches[table], n)
	return n, err
}

// TestPurgeSQLDeletesOnlyTheShorterOrgsOlderRows: three organizations
// with samples and state changes 40 and 10 days old, under a 90-day
// maximum. The one at 30 days loses its 40-day-old rows in both tables, in
// batches of 10 000; its newer rows, the one without a choice and the one
// at 90 days (= the maximum, not listed) keep everything.
func TestPurgeSQLDeletesOnlyTheShorterOrgsOlderRows(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	settings := monitorsettings.NewSQLStore(d)
	now := time.Now().UTC()
	old, recent := now.AddDate(0, 0, -40), now.AddDate(0, 0, -10)
	cutoff := now.AddDate(0, 0, -30)

	short := seedOrg(t, q, "purge-short")
	none := seedOrg(t, q, "purge-none")
	atMax := seedOrg(t, q, "purge-max")
	thirty, ninety := 30, 90
	if _, err := settings.Put(db.WithOrgScope(ctx, short), short, monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: &thirty}); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Put(db.WithOrgScope(ctx, atMax), atMax, monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: &ninety}); err != nil {
		t.Fatal(err)
	}
	for _, org := range []uuid.UUID{short, none, atMax} {
		host := seedHost(t, q, org, "h-"+org.String()[:8])
		seedSamples(t, d, org, host, old, 25000)
		seedSamples(t, d, org, host, recent, 100)
		seedStateChanges(t, d, org, host, old, 30)
		seedStateChanges(t, d, org, host, recent, 20)
	}

	store := &countingStore{PurgeSQLStore: monitorsettings.NewPurgeSQLStore(d), batches: map[monitorsettings.Table][]int64{}}
	orgs, err := store.OrgsWithShorterRetention(ctx, 90)
	if err != nil || len(orgs) != 1 || orgs[0].OrgID != short || orgs[0].Days != 30 {
		t.Fatalf("OrgsWithShorterRetention(90) = %+v, %v; want only the 30-day org", orgs, err)
	}

	res := monitorsettings.NewPurger(store, nil, 90, 0, func() time.Time { return now }, nil).Round(ctx)
	if !res.Complete || res.Orgs != 1 || res.Samples != 25000 || res.StateChanges != 30 {
		t.Fatalf("result = %+v, want complete, 1 org, 25000 samples, 30 state changes", res)
	}
	var big []int64
	for _, n := range store.batches[monitorsettings.TableCheckSamples] {
		if n > monitorsettings.RetentionPurgeBatch {
			t.Fatalf("a batch deleted %d rows, more than %d", n, monitorsettings.RetentionPurgeBatch)
		}
		if n == monitorsettings.RetentionPurgeBatch {
			big = append(big, n)
		}
	}
	if len(big) != 2 {
		t.Fatalf("sample batches = %v, want two full batches of 10000 and the rest", store.batches[monitorsettings.TableCheckSamples])
	}

	for _, table := range []string{"check_samples", "check_state_changes"} {
		if older, newer := countRows(t, d, table, short, cutoff); older != 0 || newer == 0 {
			t.Errorf("%s of the 30-day org: older=%d newer=%d, want 0 older and the newer kept", table, older, newer)
		}
		for name, org := range map[string]uuid.UUID{"no choice": none, "at the maximum": atMax} {
			older, newer := countRows(t, d, table, org, cutoff)
			if older == 0 || newer == 0 {
				t.Errorf("%s of the org with %s: older=%d newer=%d, want everything kept", table, name, older, newer)
			}
		}
	}
	if older, newer := countRows(t, d, "check_samples", none, cutoff); older != 25000 || newer != 100 {
		t.Errorf("untouched org samples = %d/%d, want 25000/100", older, newer)
	}

	// A second run has nothing left to delete.
	res = monitorsettings.NewPurger(store, nil, 90, 0, func() time.Time { return now }, nil).Round(ctx)
	if !res.Complete || res.Samples != 0 || res.StateChanges != 0 {
		t.Fatalf("second run = %+v, want nothing deleted", res)
	}
}

// TestPurgeSQLKeepsRowsJustInsideTheRetention: the cutoff is exact: a row
// a minute older than 30 days goes, one a minute younger stays.
func TestPurgeSQLKeepsRowsJustInsideTheRetention(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -30)
	org := seedOrg(t, q, "purge-edge")
	thirty := 30
	if _, err := monitorsettings.NewSQLStore(d).Put(db.WithOrgScope(ctx, org), org, monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: &thirty}); err != nil {
		t.Fatal(err)
	}
	host := seedHost(t, q, org, "edge")
	seedSamples(t, d, org, host, cutoff.Add(-time.Minute), 1)
	seedSamples(t, d, org, host, cutoff.Add(time.Minute), 1)

	res := monitorsettings.NewPurger(monitorsettings.NewPurgeSQLStore(d), nil, 90, 0, func() time.Time { return now }, nil).Round(ctx)
	if res.Samples != 1 {
		t.Fatalf("deleted %d samples, want exactly the one past the cutoff", res.Samples)
	}
	if older, newer := countRows(t, d, "check_samples", org, cutoff); older != 0 || newer != 1 {
		t.Fatalf("after the purge older=%d newer=%d, want 0 and 1", older, newer)
	}
}

// TestPurgeSQLDeleteBatchStaysInItsOrg: DeleteBatch for one organization
// over a range holding another organization's rows deletes none of
// them, and a range in the future of the rows deletes nothing.
func TestPurgeSQLDeleteBatchStaysInItsOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	now := time.Now().UTC()
	at := now.AddDate(0, 0, -50)
	a := seedOrg(t, q, "purge-iso-a")
	b := seedOrg(t, q, "purge-iso-b")
	seedSamples(t, d, b, seedHost(t, q, b, "hb"), at, 50)
	seedStateChanges(t, d, b, seedHost(t, q, b, "hb2"), at, 5)
	store := monitorsettings.NewPurgeSQLStore(d)
	r := monitorsettings.TimeRange{Start: at.Add(-time.Hour), End: now}
	for _, table := range []monitorsettings.Table{monitorsettings.TableCheckSamples, monitorsettings.TableCheckStateChanges} {
		if n, err := store.DeleteBatch(ctx, table, a, r, monitorsettings.RetentionPurgeBatch); err != nil || n != 0 {
			t.Fatalf("%s DeleteBatch for org a = %d, %v; want 0", table, n, err)
		}
	}
	if older, _ := countRows(t, d, "check_samples", b, now); older != 50 {
		t.Fatalf("org b samples = %d, want 50", older)
	}
	if _, err := store.DeleteBatch(ctx, monitorsettings.Table("alerts"), b, r, 10); err == nil {
		t.Fatal("DeleteBatch on a table other than the two succeeded, want an error")
	}
}

// TestPurgeSQLChunkRanges: the ranges are the hypertable's chunks that
// start before the cutoff, oldest first, one day each.
func TestPurgeSQLChunkRanges(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	now := time.Now().UTC()
	org := seedOrg(t, q, "purge-chunks")
	host := seedHost(t, q, org, "hc")
	for _, days := range []int{60, 45, 5} {
		seedSamples(t, d, org, host, now.AddDate(0, 0, -days), 1)
	}
	cutoff := now.AddDate(0, 0, -30)
	ranges, err := monitorsettings.NewPurgeSQLStore(d).ChunkRanges(ctx, monitorsettings.TableCheckSamples, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	// Other tests of this binary may have left empty chunks behind, so
	// look for the two seeded ones rather than counting.
	covers := func(at time.Time) bool {
		for _, r := range ranges {
			if !at.Before(r.Start) && at.Before(r.End) {
				return true
			}
		}
		return false
	}
	if !covers(now.AddDate(0, 0, -60)) || !covers(now.AddDate(0, 0, -45)) || covers(now.AddDate(0, 0, -5)) {
		t.Fatalf("ranges = %+v, want the chunks of 60 and 45 days ago and not the one of 5 days ago", ranges)
	}
	for i, r := range ranges {
		if !r.Start.Before(cutoff) || r.End.Sub(r.Start) != 24*time.Hour {
			t.Fatalf("range %+v: want a one-day chunk starting before the cutoff", r)
		}
		if i > 0 && !ranges[i-1].Start.Before(r.Start) {
			t.Fatalf("ranges = %+v, want oldest first", ranges)
		}
	}
}
