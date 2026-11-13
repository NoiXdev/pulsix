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
	if err := store.Put(scoped, org, monitorsettings.Settings{ClientAbsentDays: 3}); err != nil {
		t.Fatal(err)
	}
	if st, ok, err := store.Get(scoped, org); err != nil || !ok || st.ClientAbsentDays != 3 {
		t.Fatalf("Get after Put = %+v, ok=%v, %v; want 3", st, ok, err)
	}
	if err := store.Put(scoped, org, monitorsettings.Settings{ClientAbsentDays: 0}); err != nil {
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
	if err := store.Put(otherScoped, org, monitorsettings.Settings{ClientAbsentDays: 99}); err == nil {
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
		if err := store.Put(db.WithOrgScope(ctx, org), org, monitorsettings.Settings{ClientAbsentDays: days}); err == nil {
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
	if err := store.Put(db.WithOrgScope(ctx, org), org, monitorsettings.Settings{ClientAbsentDays: 0}); err != nil {
		t.Fatal(err)
	}
	if days, err := q.GetOrgClientAbsentDays(ctx, org); err != nil || days != 0 {
		t.Fatalf("ClientAbsentDays after storing 0 = %d, %v; want 0", days, err)
	}
}
