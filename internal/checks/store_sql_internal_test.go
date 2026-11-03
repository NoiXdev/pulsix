package checks

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func float64Ptr(v float64) *float64 { return &v }

// TestRecordHostObservationsSavepointIsolatesAForeignKeyViolation pins the
// mechanism RecordObservations' host-existence race fix relies on
// (review finding 1): a foreign-key violation on one host's insert —
// simulated here with a host id that was never created, which fails the
// exact same (org_id, host_id) foreign key a deleted one would — rolls
// back only that host's own savepoint, reporting ok=false, err=nil,
// while a different host's own savepoint (already committed earlier in
// the same outer transaction) is left completely intact, and the outer
// transaction itself remains usable afterward.
//
// This is deliberately not a reproduction of the literal microsecond-
// scale interleaving a real race would need (a concurrent DELETE landing
// exactly between RecordObservations' own HostsExisting query and its
// insert): there is no seam in RecordObservations to pause execution at
// that exact point without adding a testing-only hook to production
// code, and no way to make a goroutine win that race deterministically
// without one either — both felt like the "unreasonable contortion" the
// review anticipated. This is the closest honest substitute: it drives
// recordHostObservations, the exact private helper the fix introduced,
// through the exact failure branch (a foreign-key violation caught and
// turned into ok=false rather than propagated) a real race would also
// take, since a foreign-key check cannot tell "deleted a moment ago"
// apart from "never existed" — both are simply "no matching row right
// now".
func TestRecordHostObservationsSavepointIsolatesAForeignKeyViolation(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())

	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "checks-savepoint", Name: "checks-savepoint"})
	if err != nil {
		t.Fatal(err)
	}
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone.ID, Name: "h", Facts: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	missingHost := uuid.Must(uuid.NewV7()) // never created — the same FK failure a deleted host would produce

	scoped := db.WithOrgScope(ctx, org.ID)
	now := time.Now()

	err = d.WithOrg(scoped, org.ID, func(tx pgx.Tx) error {
		// The real host's own observation commits in its own savepoint
		// first, exactly as a normal batch would.
		_, ok, err := recordHostObservations(ctx, tx, org.ID, host.ID, []Observation{
			{HostID: host.ID, CheckKey: "cpu", Metric: "usage", At: now, Value: float64Ptr(1)},
		})
		if err != nil || !ok {
			t.Fatalf("real host: ok=%v err=%v, want ok=true, err=nil", ok, err)
		}

		// The missing host's insert fails its foreign key. The savepoint
		// this call opened is rolled back internally — ok=false, err=nil
		// — and, crucially, tx itself must still be usable afterward: a
		// savepoint failure must never abort the transaction it is
		// nested in.
		_, ok, err = recordHostObservations(ctx, tx, org.ID, missingHost, []Observation{
			{HostID: missingHost, CheckKey: "cpu", Metric: "usage", At: now, Value: float64Ptr(2)},
		})
		if err != nil {
			t.Fatalf("missing host: err = %v, want nil (a caught foreign-key violation, not a propagated error)", err)
		}
		if ok {
			t.Fatal("missing host: ok = true, want false")
		}

		if _, err := gen.New(tx).HostsExisting(ctx, gen.HostsExistingParams{OrgID: org.ID, HostIds: []uuid.UUID{host.ID}}); err != nil {
			t.Fatalf("tx must still be usable after the missing host's savepoint rolled back: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The real host's row survived the outer commit; the missing host
	// never had one to survive or not.
	st := NewSQLStore(d)
	current, err := st.CurrentForHost(scoped, org.ID, host.ID)
	if err != nil || len(current) != 1 {
		t.Fatalf("current = %+v, %v, want exactly the real host's one row committed", current, err)
	}
}
