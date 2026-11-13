package monitorsettings_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/monitorsettings"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// TestSQLStoreRoundTripAndIsolation: no row reads as absent, a write
// round-trips and an overwrite replaces it, and inside one organization's
// scope another organization's row is invisible.
func TestSQLStoreRoundTripAndIsolation(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := monitorsettings.NewSQLStore(d)
	org := seedOrg(t, q, "monset-org")
	other := seedOrg(t, q, "monset-other")
	scoped := db.WithOrgScope(ctx, org)
	otherScoped := db.WithOrgScope(ctx, other)

	if _, ok, err := store.Get(scoped, org); err != nil || ok {
		t.Fatalf("Get before any write: ok=%v, %v; want no row", ok, err)
	}
	if _, err := store.Put(scoped, org, monitorsettings.Update{ClientAbsentDays: 3}); err != nil {
		t.Fatal(err)
	}
	if st, ok, err := store.Get(scoped, org); err != nil || !ok || st.ClientAbsentDays != 3 {
		t.Fatalf("Get after Put = %+v, ok=%v, %v; want 3", st, ok, err)
	}
	if _, err := store.Put(scoped, org, monitorsettings.Update{ClientAbsentDays: 0}); err != nil {
		t.Fatal(err)
	}
	if st, _, err := store.Get(scoped, org); err != nil || st.ClientAbsentDays != 0 {
		t.Fatalf("Get after the overwrite = %+v, %v; want 0", st, err)
	}

	// The other organization has no row, and cannot read org's.
	if _, ok, err := store.Get(otherScoped, other); err != nil || ok {
		t.Fatalf("other org Get: ok=%v, %v; want no row", ok, err)
	}
	if _, ok, err := store.Get(otherScoped, org); err != nil || ok {
		t.Fatalf("org's row read inside the other org's scope: ok=%v, %v; want invisible", ok, err)
	}
	// Nor write it: the upsert inside the other scope cannot touch org's row.
	if _, err := store.Put(otherScoped, org, monitorsettings.Update{ClientAbsentDays: 99}); err == nil {
		t.Fatal("Put of org's row inside the other org's scope succeeded, want a row-level security refusal")
	}
	if st, _, _ := store.Get(scoped, org); st.ClientAbsentDays != 0 {
		t.Fatalf("org's row after the other org's write = %+v, want 0", st)
	}
}

// TestSQLStoreRefusesOutOfRange: the store refuses an out-of-range value,
// and the table's CHECK holds for a writer that skips the store.
func TestSQLStoreRefusesOutOfRange(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := monitorsettings.NewSQLStore(d)
	org := seedOrg(t, q, "monset-range-org")
	for _, days := range []int{-1, 366} {
		if _, err := store.Put(db.WithOrgScope(ctx, org), org, monitorsettings.Update{ClientAbsentDays: days}); err == nil {
			t.Errorf("Put(%d) succeeded, want a refusal", days)
		}
		if _, err := d.Pool().Exec(ctx, `INSERT INTO org_monitoring_settings (org_id, client_absent_days) VALUES ($1, $2)`, org, days); err == nil {
			t.Errorf("raw insert of %d succeeded, want the CHECK to refuse it", days)
		}
	}
}

// TestDefaultIsPinnedAcrossSchemaQueryAndCode: the default lives in the
// column default (00091), in GetOrgClientAbsentDays' COALESCE and in
// DefaultClientAbsentDays; the three must agree.
func TestDefaultIsPinnedAcrossSchemaQueryAndCode(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := monitorsettings.NewSQLStore(d)
	org := seedOrg(t, q, "monset-default-org")

	// No row: the evaluator's read (internal/rules' Load) applies the
	// default.
	days, err := q.GetOrgClientAbsentDays(ctx, org)
	if err != nil || days != monitorsettings.DefaultClientAbsentDays {
		t.Fatalf("ClientAbsentDays without a row = %d, %v; want %d", days, err, monitorsettings.DefaultClientAbsentDays)
	}
	// A row created with the column default holds the same value.
	if _, err := d.Pool().Exec(ctx, `INSERT INTO org_monitoring_settings (org_id) VALUES ($1)`, org); err != nil {
		t.Fatal(err)
	}
	if st, ok, err := store.Get(db.WithOrgScope(ctx, org), org); err != nil || !ok || st.ClientAbsentDays != monitorsettings.DefaultClientAbsentDays {
		t.Fatalf("column default = %+v, ok=%v, %v; want %d", st, ok, err, monitorsettings.DefaultClientAbsentDays)
	}
	// A stored value wins over the default, also outside any org scope
	// (the evaluation ticker reads across organizations).
	if _, err := store.Put(db.WithOrgScope(ctx, org), org, monitorsettings.Update{ClientAbsentDays: 0}); err != nil {
		t.Fatal(err)
	}
	if days, err := q.GetOrgClientAbsentDays(ctx, org); err != nil || days != 0 {
		t.Fatalf("ClientAbsentDays after storing 0 = %d, %v; want 0", days, err)
	}
}

// TestSQLStoreRetentionKeepAndClear: a new row starts without a retention
// choice, a choice round-trips, an update without one keeps it (the SPA's
// absent-days form sends only client_absent_days), and an explicit clear
// sets it back to NULL.
func TestSQLStoreRetentionKeepAndClear(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := monitorsettings.NewSQLStore(d)
	org := seedOrg(t, q, "monset-retention-org")
	scoped := db.WithOrgScope(ctx, org)
	days := func(n int) *int { return &n }

	got, err := store.Put(scoped, org, monitorsettings.Update{ClientAbsentDays: 5})
	if err != nil || got.ClientAbsentDays != 5 || got.RetentionDays != 0 {
		t.Fatalf("first Put without a choice = %+v, %v; want 5 and no choice", got, err)
	}
	got, err = store.Put(scoped, org, monitorsettings.Update{ClientAbsentDays: 5, RetentionDays: days(30)})
	if err != nil || got.RetentionDays != 30 || got.PreviousRetentionDays != 0 {
		t.Fatalf("Put(30) = %+v, %v", got, err)
	}
	got, err = store.Put(scoped, org, monitorsettings.Update{ClientAbsentDays: 9})
	if err != nil || got.ClientAbsentDays != 9 || got.RetentionDays != 30 {
		t.Fatalf("Put without a choice = %+v, %v; want 9 and the kept 30", got, err)
	}
	if st, ok, err := store.Get(scoped, org); err != nil || !ok || st.RetentionDays != 30 || st.ClientAbsentDays != 9 {
		t.Fatalf("Get = %+v, ok=%v, %v; want 9 and 30", st, ok, err)
	}
	got, err = store.Put(scoped, org, monitorsettings.Update{ClientAbsentDays: 9, RetentionDays: days(0)})
	if err != nil || got.RetentionDays != 0 || got.PreviousRetentionDays != 30 {
		t.Fatalf("Put(clear) = %+v, %v; want no choice, replacing 30", got, err)
	}
	var stored *int32
	if err := d.Pool().QueryRow(ctx, `SELECT retention_days FROM org_monitoring_settings WHERE org_id = $1`, org).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("stored retention_days after clear = %v, %v; want NULL", stored, err)
	}

	// A first write that carries a choice creates the row with it.
	fresh := seedOrg(t, q, "monset-retention-fresh")
	got, err = store.Put(db.WithOrgScope(ctx, fresh), fresh, monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: days(365)})
	if err != nil || got.RetentionDays != 365 || got.ClientAbsentDays != 14 {
		t.Fatalf("first Put(365) = %+v, %v", got, err)
	}
}

// TestRetentionCheck: the table admits NULL, 30, 90 and 365 only, and the
// store refuses anything else before the database sees it.
func TestRetentionCheck(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := monitorsettings.NewSQLStore(d)
	org := seedOrg(t, q, "monset-retention-check")
	for _, days := range []int{30, 90, 365} {
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO org_monitoring_settings (org_id, retention_days) VALUES ($1, $2)
			ON CONFLICT (org_id) DO UPDATE SET retention_days = EXCLUDED.retention_days`, org, days); err != nil {
			t.Errorf("raw write of %d: %v, want it admitted", days, err)
		}
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE org_monitoring_settings SET retention_days = NULL WHERE org_id = $1`, org); err != nil {
		t.Errorf("raw write of NULL: %v, want it admitted", err)
	}
	for _, days := range []int{0, 7, 29, 60, 364, 366, -30} {
		if _, err := d.Pool().Exec(ctx, `UPDATE org_monitoring_settings SET retention_days = $2 WHERE org_id = $1`, org, days); err == nil {
			t.Errorf("raw write of %d succeeded, want the CHECK to refuse it", days)
		}
		n := days
		if _, err := store.Put(db.WithOrgScope(ctx, org), org, monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: &n}); err == nil && days != 0 {
			t.Errorf("store Put(retention %d) succeeded, want a refusal", days)
		}
	}
}
