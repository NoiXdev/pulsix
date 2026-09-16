package inventory_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// TestUpdateGroupSerializesConcurrentMoves is backlog 2.15's group-move
// race: for many independent (root, A, B) triples, one goroutine moves A
// under B while another concurrently moves B under A. Without the
// per-organization advisory lock (SQLStore.WithTreeLock) both calls can
// read the tree before either write lands, see no cycle, and both commit —
// leaving A and B pointing at each other with no path to the root. With the
// lock, whichever call commits second re-reads the tree after the first
// has committed and is rejected as a self-descendant move.
//
// A single pair races unreliably in practice (see task-16-report.md: with
// the lock disabled, one pair alone passed 5/5 runs by luck — local
// round trips are fast enough that one goroutine's whole transaction often
// finishes before the other's first query fires). Racing many independent
// pairs at once, released through a shared start barrier, reproduced the
// corruption reliably (16/40 pairs) against the unlocked code and is the
// form kept here so a regression is actually caught, not just possibly
// caught.
func TestUpdateGroupSerializesConcurrentMoves(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := inventory.NewSQLStore(d)
	hostStore := hosts.NewSQLStore(d)
	sshStore := sshconf.NewSQLStore(d)
	svc := inventory.NewService(store, hostStore, sshStore, testBox())

	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "tree-race", Name: "tree-race"})
	if err != nil {
		t.Fatal(err)
	}

	const pairs = 24
	type pair struct{ root, a, b inventory.GroupView }
	ps := make([]pair, pairs)
	for i := range ps {
		suffix := uuid.Must(uuid.NewV7()).String()[24:]
		root, err := svc.CreateGroup(ctx, org.ID, inventory.GroupInput{Name: "root_" + suffix})
		if err != nil {
			t.Fatalf("create root %d: %v", i, err)
		}
		a, err := svc.CreateGroup(ctx, org.ID, inventory.GroupInput{Name: "a_" + suffix, ParentID: &root.ID})
		if err != nil {
			t.Fatalf("create a %d: %v", i, err)
		}
		b, err := svc.CreateGroup(ctx, org.ID, inventory.GroupInput{Name: "b_" + suffix, ParentID: &root.ID})
		if err != nil {
			t.Fatalf("create b %d: %v", i, err)
		}
		ps[i] = pair{root, a, b}
	}

	var wg sync.WaitGroup
	errsA := make([]error, pairs)
	errsB := make([]error, pairs)
	start := make(chan struct{})
	for i, p := range ps {
		wg.Add(2)
		go func(i int, p pair) {
			defer wg.Done()
			<-start
			_, errsA[i] = svc.UpdateGroup(ctx, org.ID, p.a.ID, p.a.Revision, inventory.GroupPatch{Name: p.a.Name, ParentID: &p.b.ID})
		}(i, p)
		go func(i int, p pair) {
			defer wg.Done()
			<-start
			_, errsB[i] = svc.UpdateGroup(ctx, org.ID, p.b.ID, p.b.Revision, inventory.GroupPatch{Name: p.b.Name, ParentID: &p.a.ID})
		}(i, p)
	}
	close(start)
	wg.Wait()

	for i := range ps {
		aFailed := errsA[i] != nil && errors.Is(errsA[i], inventory.ErrInvalidInput)
		bFailed := errsB[i] != nil && errors.Is(errsB[i], inventory.ErrInvalidInput)
		if errsA[i] != nil && !aFailed {
			t.Fatalf("pair %d: A: unexpected error %v", i, errsA[i])
		}
		if errsB[i] != nil && !bFailed {
			t.Fatalf("pair %d: B: unexpected error %v", i, errsB[i])
		}
		if aFailed == bFailed {
			t.Fatalf("pair %d: expected exactly one move to be rejected as a cycle, got errA=%v errB=%v", i, errsA[i], errsB[i])
		}
	}

	// Whichever move won each pair, every group must still reach a root
	// within a bounded number of hops: a cycle would spin forever, so cap
	// the walk and fail loudly instead of hanging the test.
	groups, err := store.ListGroups(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[uuid.UUID]inventory.Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	for _, p := range ps {
		for _, start := range []uuid.UUID{p.a.ID, p.b.ID} {
			cur := start
			for hops := 0; ; hops++ {
				if hops > len(groups) {
					t.Fatalf("group %s's ancestor chain does not reach a root: a cycle was committed", start)
				}
				g, ok := byID[cur]
				if !ok {
					t.Fatalf("group %s vanished from the tree", cur)
				}
				if g.ParentID == nil {
					break
				}
				cur = *g.ParentID
			}
		}
	}
}
