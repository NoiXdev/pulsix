// ReserveRunGroup against the real store (package jobs_test): the ON
// CONFLICT round trip migration 00067 added for RunRequest.IdempotencyKey
// (backlog 2.60), isolated from the rest of EnqueueRunWith so a failure here
// points straight at the SQL, not at the scope resolution or job creation
// around it.
package jobs_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestReserveRunGroupClaimsOnceAndReplaysAfter pins the two-outcome contract
// ReserveRunGroup's own doc comment describes: the first call for a key
// reports reserved = true and its own candidate back; every later call for
// the same (org, key) reports reserved = false and the FIRST candidate back,
// never its own — which is what lets EnqueueRunWith tell "I just claimed
// this" from "someone already did" in one round trip.
func TestReserveRunGroupClaimsOnceAndReplaysAfter(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-idempotency-claim")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	firstCandidate := uuid.Must(uuid.NewV7())
	group, reserved, err := store.ReserveRunGroup(scoped, org, "schedule:x:2026-09-18T10:00:00Z", firstCandidate)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if !reserved || group != firstCandidate {
		t.Fatalf("first reserve = (group %s, reserved %v), want (%s, true)", group, reserved, firstCandidate)
	}

	secondCandidate := uuid.Must(uuid.NewV7())
	group, reserved, err = store.ReserveRunGroup(scoped, org, "schedule:x:2026-09-18T10:00:00Z", secondCandidate)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if reserved || group != firstCandidate {
		t.Fatalf("second reserve = (group %s, reserved %v), want (%s, false) — a retry must get the FIRST candidate back, not its own", group, reserved, firstCandidate)
	}

	// A different key in the same organization claims independently.
	otherCandidate := uuid.Must(uuid.NewV7())
	group, reserved, err = store.ReserveRunGroup(scoped, org, "schedule:x:2026-09-18T11:00:00Z", otherCandidate)
	if err != nil {
		t.Fatalf("different-key reserve: %v", err)
	}
	if !reserved || group != otherCandidate {
		t.Fatalf("different-key reserve = (group %s, reserved %v), want (%s, true)", group, reserved, otherCandidate)
	}
}

// TestReserveRunGroupIsScopedPerOrganization: the same idempotency key
// string in two different organizations must not collide — the primary key
// is (org_id, idempotency_key), not idempotency_key alone, and RLS must let
// each organization see only its own row.
func TestReserveRunGroupIsScopedPerOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	orgA := seedOrg(t, q, "jobs-idempotency-org-a")
	orgB := seedOrg(t, q, "jobs-idempotency-org-b")
	store := jobs.NewSQLStore(d)

	key := "schedule:shared-key:2026-09-18T10:00:00Z"
	candidateA := uuid.Must(uuid.NewV7())
	groupA, reservedA, err := store.ReserveRunGroup(db.WithOrgScope(ctx, orgA), orgA, key, candidateA)
	if err != nil || !reservedA || groupA != candidateA {
		t.Fatalf("org A reserve = (group %s, reserved %v, err %v), want (%s, true, nil)", groupA, reservedA, err, candidateA)
	}

	candidateB := uuid.Must(uuid.NewV7())
	groupB, reservedB, err := store.ReserveRunGroup(db.WithOrgScope(ctx, orgB), orgB, key, candidateB)
	if err != nil || !reservedB || groupB != candidateB {
		t.Fatalf("org B reserve = (group %s, reserved %v, err %v), want (%s, true, nil) — the same key string in a different org must claim independently", groupB, reservedB, err, candidateB)
	}
}

// TestStartedRunGroupForKey: a claimed key names its run group only once
// that group has a run job; an unclaimed key and a claim that created
// nothing answer nil.
func TestStartedRunGroupForKey(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-idempotency-started")
	zone := seedZone(t, d, org)
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	if got, err := store.StartedRunGroup(scoped, org, "chain:a"); err != nil || got != nil {
		t.Fatalf("unclaimed = %v, %v, want nil", got, err)
	}
	group := uuid.Must(uuid.NewV7())
	if _, _, err := store.ReserveRunGroup(scoped, org, "chain:a", group); err != nil {
		t.Fatal(err)
	}
	if got, err := store.StartedRunGroup(scoped, org, "chain:a"); err != nil || got != nil {
		t.Fatalf("claimed without jobs = %v, %v, want nil", got, err)
	}
	if _, err := d.Pool().Exec(ctx, `INSERT INTO jobs (id, org_id, zone_id, kind, host_ids, status, run_group_id)
		VALUES ($1,$2,$3,'run','{}','queued',$4)`, uuid.Must(uuid.NewV7()), org, zone, group); err != nil {
		t.Fatal(err)
	}
	got, err := store.StartedRunGroup(scoped, org, "chain:a")
	if err != nil || got == nil || *got != group {
		t.Fatalf("started = %v, %v, want %s", got, err, group)
	}
	if other, err := store.StartedRunGroup(db.WithOrgScope(ctx, uuid.Must(uuid.NewV7())), uuid.Must(uuid.NewV7()), "chain:a"); err != nil || other != nil {
		t.Fatalf("another org = %v, %v, want nil", other, err)
	}
}
