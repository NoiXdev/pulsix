package sshconf_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// TestUpdateBastionSerializesConcurrentChainEdits is backlog 2.15's
// jump-host chain race: for many independent (A, B) pairs of bastions with
// no jump host of their own, one goroutine points A via B while another
// concurrently points B via A. Without the per-organization advisory lock
// (SQLStore.WithChainLock) both calls can list the organization's bastions
// before either write lands, see no cycle, and both commit — leaving A and
// B chained through each other with no terminal hop. With the lock,
// whichever call commits second re-lists the bastions after the first has
// committed and is rejected as a cycle.
//
// Many independent pairs are raced at once (see
// inventory.TestUpdateGroupSerializesConcurrentMoves, same rationale): a
// single pair does not reproduce the corruption reliably given how fast a
// local round trip is relative to goroutine scheduling.
func TestUpdateBastionSerializesConcurrentChainEdits(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := sshconf.NewSQLStore(d)
	credStore := credentials.NewSQLStore(d)
	hostStore := hosts.NewSQLStore(d)
	svc := sshconf.NewService(store, credStore, hostStore)

	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "chain-race", Name: "chain-race"})
	if err != nil {
		t.Fatal(err)
	}

	const pairs = 24
	type pair struct{ a, b sshconf.Bastion }
	ps := make([]pair, pairs)
	for i := range ps {
		suffix := uuid.Must(uuid.NewV7()).String()[24:]
		a, err := svc.CreateBastion(ctx, org.ID, sshconf.BastionInput{Name: "a-" + suffix, Address: "a-" + suffix + ".example", Username: "ops"})
		if err != nil {
			t.Fatalf("create a %d: %v", i, err)
		}
		b, err := svc.CreateBastion(ctx, org.ID, sshconf.BastionInput{Name: "b-" + suffix, Address: "b-" + suffix + ".example", Username: "ops"})
		if err != nil {
			t.Fatalf("create b %d: %v", i, err)
		}
		ps[i] = pair{a, b}
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
			_, errsA[i] = svc.UpdateBastion(ctx, org.ID, p.a.ID, sshconf.BastionInput{Name: p.a.Name, Address: p.a.Address, Username: p.a.Username, ViaBastionID: &p.b.ID})
		}(i, p)
		go func(i int, p pair) {
			defer wg.Done()
			<-start
			_, errsB[i] = svc.UpdateBastion(ctx, org.ID, p.b.ID, sshconf.BastionInput{Name: p.b.Name, Address: p.b.Address, Username: p.b.Username, ViaBastionID: &p.a.ID})
		}(i, p)
	}
	close(start)
	wg.Wait()

	for i := range ps {
		aFailed := errsA[i] != nil && errors.Is(errsA[i], sshconf.ErrInvalidInput)
		bFailed := errsB[i] != nil && errors.Is(errsB[i], sshconf.ErrInvalidInput)
		if errsA[i] != nil && !aFailed {
			t.Fatalf("pair %d: A: unexpected error %v", i, errsA[i])
		}
		if errsB[i] != nil && !bFailed {
			t.Fatalf("pair %d: B: unexpected error %v", i, errsB[i])
		}
		if aFailed == bFailed {
			t.Fatalf("pair %d: expected exactly one chain edit to be rejected as a cycle, got errA=%v errB=%v", i, errsA[i], errsB[i])
		}
	}

	// Whichever edit won each pair, both bastions' chains must resolve
	// without a cycle and within sshconf.MaxChain hops.
	all, err := store.ListBastions(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[uuid.UUID]sshconf.Bastion, len(all))
	for _, b := range all {
		byID[b.ID] = b
	}
	for _, p := range ps {
		for _, id := range []uuid.UUID{p.a.ID, p.b.ID} {
			if _, err := sshconf.Chain(byID, id); err != nil {
				t.Fatalf("bastion %s: %v (a cycle or an over-long chain was committed)", id, err)
			}
		}
	}
}
