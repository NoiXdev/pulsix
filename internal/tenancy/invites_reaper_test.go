package tenancy_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type fakeInviteReaperStore struct {
	mu   sync.Mutex
	cuts []time.Time
	n    int64
	err  error
}

func (f *fakeInviteReaperStore) DeleteStaleInvites(_ context.Context, expiredBefore time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cuts = append(f.cuts, expiredBefore)
	return f.n, f.err
}

func (f *fakeInviteReaperStore) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cuts)
}

type fakeInviteLocker struct {
	held   bool
	err    error
	events []string
}

func (l *fakeInviteLocker) TryLock(context.Context) (bool, error) {
	l.events = append(l.events, "try")
	return l.held, l.err
}

func (l *fakeInviteLocker) Unlock(context.Context) { l.events = append(l.events, "unlock") }

func inviteReaperClock() func() time.Time {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func TestInviteReaperSweepCutoffIsTheListWindow(t *testing.T) {
	now := inviteReaperClock()
	store := &fakeInviteReaperStore{n: 4}
	n, err := tenancy.NewInviteReaper(store, nil, 0, now, nil).Sweep(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("Sweep = %d, %v; want 4, nil", n, err)
	}
	want := now().Add(-30 * 24 * time.Hour)
	if len(store.cuts) != 1 || !store.cuts[0].Equal(want) {
		t.Fatalf("cutoffs = %v, want one at now - 30d = %v", store.cuts, want)
	}
}

func TestInviteReaperHoldsTheLockAcrossTheDelete(t *testing.T) {
	store := &fakeInviteReaperStore{}
	locker := &fakeInviteLocker{held: true}
	if _, err := tenancy.NewInviteReaper(store, locker, 0, nil, nil).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.calls() != 1 || len(locker.events) != 2 || locker.events[0] != "try" || locker.events[1] != "unlock" {
		t.Fatalf("calls = %d, lock events = %v; want one delete between try and unlock", store.calls(), locker.events)
	}
}

func TestInviteReaperSkipsWhenTheLockIsHeldElsewhere(t *testing.T) {
	store := &fakeInviteReaperStore{n: 5}
	locker := &fakeInviteLocker{held: false}
	n, err := tenancy.NewInviteReaper(store, locker, 0, nil, nil).Sweep(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("Sweep = %d, %v; want 0, nil", n, err)
	}
	if store.calls() != 0 || len(locker.events) != 1 {
		t.Fatalf("calls = %d, lock events = %v; want no delete and no unlock", store.calls(), locker.events)
	}
}

func TestInviteReaperReportsALockError(t *testing.T) {
	store := &fakeInviteReaperStore{}
	locker := &fakeInviteLocker{err: errors.New("pool exhausted")}
	if _, err := tenancy.NewInviteReaper(store, locker, 0, nil, nil).Sweep(context.Background()); !errors.Is(err, locker.err) {
		t.Fatalf("Sweep err = %v, want the locker's error wrapped", err)
	}
	if store.calls() != 0 {
		t.Fatal("store must not be touched when the lock cannot be acquired")
	}
}

func TestInviteReaperWrapsAStoreError(t *testing.T) {
	store := &fakeInviteReaperStore{n: 3, err: errors.New("delete failed")}
	n, err := tenancy.NewInviteReaper(store, nil, 0, nil, nil).Sweep(context.Background())
	if !errors.Is(err, store.err) || n != 0 {
		t.Fatalf("Sweep = %d, %v; want 0 and the store's error wrapped", n, err)
	}
}

func TestInviteReaperRunTicksAndSurvivesAnError(t *testing.T) {
	store := &fakeInviteReaperStore{err: errors.New("delete failed")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		tenancy.NewInviteReaper(store, nil, time.Millisecond, nil, nil).Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for store.calls() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("the reaper stopped ticking after a failing sweep")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run must return when the context is done")
	}
}

// TestDeleteStaleInvitesAgainstTheDatabase: the real query removes only
// unaccepted invitations that expired before the cutoff, in every
// organization, and the count matches.
func TestDeleteStaleInvitesAgainstTheDatabase(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)

	now := time.Now().UTC().Truncate(time.Microsecond)
	day := 24 * time.Hour
	type row struct {
		id       uuid.UUID
		keep     bool
		accepted bool
		expires  time.Time
	}
	var rows []row
	for _, slug := range []string{"reap-a", "reap-b"} {
		org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug}
		if err := store.CreateOrg(ctx, org); err != nil {
			t.Fatal(err)
		}
		inviter := seedUser(t, q, slug+"@example.test")
		for i, c := range []row{
			{keep: true, accepted: true, expires: now.Add(-400 * day)},
			{keep: false, expires: now.Add(-31 * day)},
			{keep: true, expires: now.Add(-29 * day)},
			{keep: true, expires: now.Add(day)},
		} {
			c.id = uuid.Must(uuid.NewV7())
			rows = append(rows, c)
			var accepted any
			if c.accepted {
				accepted = now.Add(-401 * day)
			}
			if _, err := d.Pool().Exec(ctx,
				`INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at, accepted_at)
				 VALUES ($1, $2, $3, 'operator', $4, $5, $6, $7)`,
				c.id, org.ID, slug+string(rune('a'+i))+"@invitee.test", hashOf(c.id.String()), inviter, c.expires, accepted); err != nil {
				t.Fatal(err)
			}
		}
	}

	n, err := tenancy.NewInviteReaper(q, nil, 0, func() time.Time { return now }, nil).Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("Sweep removed %d, want 2 (one per organization)", n)
	}
	for _, r := range rows {
		var c int
		if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM invites WHERE id = $1`, r.id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 1, false: 0}[r.keep]; c != want {
			t.Fatalf("invite expiring %v accepted=%v: rows = %d, want %d", r.expires, r.accepted, c, want)
		}
	}
}
