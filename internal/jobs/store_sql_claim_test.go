package jobs_test

import (
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestQueueClaimIsScopedToTheWorkersOrganization (fix round 1, Critical):
// the org-unscoped claim is still bound to the claiming worker's
// organization. A worker of org A naming org B's zone id claims nothing —
// org B's job stays queued and its payload never leaves the database —
// while org B's own worker claims it normally.
func TestQueueClaimIsScopedToTheWorkersOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	orgA := seedOrg(t, q, "jobs-claim-scope-a")
	orgB := seedOrg(t, q, "jobs-claim-scope-b")
	store := jobs.NewSQLStore(d)
	queue := jobs.NewSQLQueue(d, fakeSealer{}, emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader)
	queue.SetKeyPins(emptyPins{})

	zoneB := seedZone(t, d, orgB)
	jobB := newJob(orgB, zoneB, jobs.KindAdhoc, jobs.StatusQueued)
	scopedB := db.WithOrgScope(ctx, orgB)
	if err := store.CreateJob(scopedB, jobB, nil); err != nil {
		t.Fatal(err)
	}

	claim, err := queue.Claim(ctx, orgA, []uuid.UUID{zoneB}, "v1")
	if err != nil {
		t.Fatalf("foreign claim: %v", err)
	}
	if claim != nil {
		t.Fatalf("a worker of org A claimed org B's job %s", claim.Job.GetId())
	}
	row, err := store.GetJob(scopedB, orgB, jobB.ID)
	if err != nil || row.Status != jobs.StatusQueued {
		t.Fatalf("org B's job after the foreign claim = %+v, %v; want still queued", row, err)
	}

	claim, err = queue.Claim(ctx, orgB, []uuid.UUID{zoneB}, "v1")
	if err != nil || claim == nil || claim.Job.GetId() != jobB.ID.String() {
		t.Fatalf("org B's own claim = %+v, %v; want job %s", claim, err, jobB.ID)
	}
}

// TestQueueClaimConcurrency is the brief's required test: eight parallel
// claimers against eight queued jobs of one zone claim exactly eight
// distinct jobs with no double claim; a drained queue claims nothing; and
// with jobs spread over two zones every claimer still gets one.
func TestQueueClaimConcurrency(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-claim-org")
	store := jobs.NewSQLStore(d)
	queue := jobs.NewSQLQueue(d, fakeSealer{}, emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader)
	queue.SetKeyPins(emptyPins{})

	claimN := func(zones []uuid.UUID, n int) map[uuid.UUID]int {
		var mu sync.Mutex
		claimed := map[uuid.UUID]int{}
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := queue.Claim(ctx, org, zones, "v1")
				if err != nil {
					t.Errorf("Claim: %v", err)
					return
				}
				if c == nil {
					return
				}
				id, err := uuid.Parse(c.Job.GetId())
				if err != nil {
					t.Errorf("claimed job id: %v", err)
					return
				}
				mu.Lock()
				claimed[id]++
				mu.Unlock()
			}()
		}
		wg.Wait()
		return claimed
	}

	// Phase 1: eight jobs in one zone, eight claimers.
	zoneA := seedZone(t, d, org)
	scoped := db.WithOrgScope(ctx, org)
	for i := 0; i < 8; i++ {
		if err := store.CreateJob(scoped, newJob(org, zoneA, jobs.KindAdhoc, jobs.StatusQueued), nil); err != nil {
			t.Fatal(err)
		}
	}
	claimed := claimN([]uuid.UUID{zoneA}, 8)
	if len(claimed) != 8 {
		t.Fatalf("claimed %d distinct jobs, want 8: %v", len(claimed), claimed)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Fatalf("job %s claimed %d times", id, n)
		}
		row, err := store.GetJob(scoped, org, id)
		if err != nil || row.Status != jobs.StatusRunning || row.ClaimedAt == nil || row.StartedAt == nil {
			t.Fatalf("claimed job %s = %+v, %v", id, row, err)
		}
	}

	// Phase 2: the queue is drained — every claim comes up empty.
	if c := claimN([]uuid.UUID{zoneA}, 4); len(c) != 0 {
		t.Fatalf("drained queue claimed %v", c)
	}

	// Phase 3: eight jobs across two zones, eight claimers — every claimer
	// gets one, from both zones.
	zoneB := seedZone(t, d, org)
	for i := 0; i < 4; i++ {
		if err := store.CreateJob(scoped, newJob(org, zoneA, jobs.KindAdhoc, jobs.StatusQueued), nil); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateJob(scoped, newJob(org, zoneB, jobs.KindAdhoc, jobs.StatusQueued), nil); err != nil {
			t.Fatal(err)
		}
	}
	claimed = claimN([]uuid.UUID{zoneA, zoneB}, 8)
	if len(claimed) != 8 {
		t.Fatalf("claimed %d distinct jobs across two zones, want 8: %v", len(claimed), claimed)
	}
	zones := map[uuid.UUID]bool{}
	for id := range claimed {
		row, err := store.GetJob(scoped, org, id)
		if err != nil {
			t.Fatal(err)
		}
		zones[row.ZoneID] = true
	}
	if len(zones) != 2 {
		t.Fatalf("claims covered %d zones, want both", len(zones))
	}
}
