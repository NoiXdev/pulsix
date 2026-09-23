package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/auth"
)

var ctx = context.Background()

// fakeClock returns an injectable clock pinned to one instant, so the
// sweep's cutoffs are exact, not wall-clock dependent.
func fakeClock() func() time.Time {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

// fakeReaperStore records every delete call and its cutoff, answering with
// the configured row counts (or an error) so the sweep's accounting and
// error propagation can be pinned without a database.
type fakeReaperStore struct {
	mu           sync.Mutex
	ceremonyCuts []time.Time
	sessionCuts  []time.Time
	ceremonies   int64
	sessions     int64
	errCeremony  error
	errSession   error
}

func (f *fakeReaperStore) DeleteExpiredCeremonies(_ context.Context, expiredBefore time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ceremonyCuts = append(f.ceremonyCuts, expiredBefore)
	return f.ceremonies, f.errCeremony
}

func (f *fakeReaperStore) DeleteExpiredSessions(_ context.Context, expiredBefore time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionCuts = append(f.sessionCuts, expiredBefore)
	return f.sessions, f.errSession
}

func (f *fakeReaperStore) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ceremonyCuts), len(f.sessionCuts)
}

// fakeReaperLocker records the lock lifecycle so the sweep's
// acquire-before-work and release-after-work ordering is pinned.
type fakeReaperLocker struct {
	mu       sync.Mutex
	held     bool // what TryLock answers
	tryLocks int
	unlocks  int
	events   []string
}

func (l *fakeReaperLocker) TryLock(context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tryLocks++
	l.events = append(l.events, "try")
	return l.held, nil
}

func (l *fakeReaperLocker) Unlock(context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unlocks++
	l.events = append(l.events, "unlock")
}

func (l *fakeReaperLocker) counts() (int, int, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tryLocks, l.unlocks, l.events
}

// TestReaperSweepSelectsRowsByThePredicates pins the two cutoffs a sweep
// passes to the store: ceremonies whose TTL (their own expires_at, written
// as begin + CeremonyTTL) is past go at the sweep's now; sessions go only
// once their expiry is past by the 7-day retention grace.
func TestReaperSweepSelectsRowsByThePredicates(t *testing.T) {
	now := fakeClock()
	store := &fakeReaperStore{ceremonies: 2, sessions: 3}
	r := auth.NewReaper(store, nil, 0, now, nil)

	ceremonies, sessions, err := r.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if ceremonies != 2 || sessions != 3 {
		t.Fatalf("Sweep = %d/%d, want the store's counts 2/3", ceremonies, sessions)
	}
	ceremonyCuts, sessionCuts := store.ceremonyCuts, store.sessionCuts
	if len(ceremonyCuts) != 1 || len(sessionCuts) != 1 {
		t.Fatalf("store calls = %d ceremonies / %d sessions, want one each", len(ceremonyCuts), len(sessionCuts))
	}
	if !ceremonyCuts[0].Equal(now()) {
		t.Fatalf("ceremony cutoff = %v, want the sweep's now %v", ceremonyCuts[0], now())
	}
	want := now().Add(-7 * 24 * time.Hour)
	if !sessionCuts[0].Equal(want) {
		t.Fatalf("session cutoff = %v, want now - 7d = %v", sessionCuts[0], want)
	}
}

// TestReaperSweepReportsThePartialCountOnError: a failing session delete
// does not hide that ceremonies were already reaped.
func TestReaperSweepReportsThePartialCountOnError(t *testing.T) {
	store := &fakeReaperStore{ceremonies: 2, errSession: errors.New("delete failed")}
	r := auth.NewReaper(store, nil, 0, nil, nil)

	ceremonies, sessions, err := r.Sweep(ctx)
	if err == nil || !errors.Is(err, store.errSession) {
		t.Fatalf("Sweep err = %v, want the store's error wrapped", err)
	}
	if ceremonies != 2 || sessions != 0 {
		t.Fatalf("Sweep = %d/%d, want 2/0", ceremonies, sessions)
	}
}

func TestReaperSweepPropagatesCeremonyError(t *testing.T) {
	store := &fakeReaperStore{errCeremony: errors.New("delete failed")}
	r := auth.NewReaper(store, nil, 0, nil, nil)

	ceremonies, sessions, err := r.Sweep(ctx)
	if err == nil || !errors.Is(err, store.errCeremony) {
		t.Fatalf("Sweep err = %v, want the store's error wrapped", err)
	}
	if ceremonies != 0 || sessions != 0 {
		t.Fatalf("Sweep = %d/%d, want nothing reported", ceremonies, sessions)
	}
}

// TestReaperSkipsWhenAnotherInstanceHoldsTheLock: a sweep that loses the
// advisory lock race does no work and reports zero — the other manager's
// sweep is already running.
func TestReaperSkipsWhenAnotherInstanceHoldsTheLock(t *testing.T) {
	store := &fakeReaperStore{ceremonies: 5, sessions: 5}
	locker := &fakeReaperLocker{held: false}
	r := auth.NewReaper(store, locker, 0, nil, nil)

	ceremonies, sessions, err := r.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if ceremonies != 0 || sessions != 0 {
		t.Fatalf("Sweep = %d/%d, want nothing deleted", ceremonies, sessions)
	}
	if n, _ := store.calls(); n != 0 {
		t.Fatalf("store called %d times after a lost lock race", n)
	}
	if tryLocks, unlocks, events := locker.counts(); tryLocks != 1 || unlocks != 0 || len(events) != 1 {
		t.Fatalf("lock events = %v, want exactly one try and no unlock", events)
	}
}

// TestReaperReleasesTheLockAfterItsWork: the sweep holds the advisory lock
// across its deletes and releases it afterwards, never during.
func TestReaperReleasesTheLockAfterItsWork(t *testing.T) {
	store := &fakeReaperStore{}
	locker := &fakeReaperLocker{held: true}
	r := auth.NewReaper(store, locker, 0, nil, nil)

	if _, _, err := r.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n, m := store.calls(); n != 1 || m != 1 {
		t.Fatalf("store calls = %d/%d, want the locked sweep to run both deletes", n, m)
	}
	if tryLocks, unlocks, events := locker.counts(); tryLocks != 1 || unlocks != 1 || events[0] != "try" || events[len(events)-1] != "unlock" {
		t.Fatalf("lock events = %v, want try before the work and unlock after", events)
	}
}

// TestReaperReportsALostLockRaceAsAnError: a locker that errors out (the
// session could not be acquired) must surface, not count as a clean skip.
func TestReaperReportsALostLockRaceAsAnError(t *testing.T) {
	store := &fakeReaperStore{}
	r := auth.NewReaper(store, errLocker{}, 0, nil, nil)

	if _, _, err := r.Sweep(ctx); err == nil {
		t.Fatal("Sweep with a failing locker must return the error")
	}
	if n, _ := store.calls(); n != 0 {
		t.Fatal("store must not be touched when the lock cannot be acquired")
	}
}

type errLocker struct{}

func (errLocker) TryLock(context.Context) (bool, error) { return false, errors.New("pool exhausted") }
func (errLocker) Unlock(context.Context)                {}

// TestReaperRunTicksOnTheInterval: the ticker fires the sweep repeatedly
// and Run returns when the context is done.
func TestReaperRunTicksOnTheInterval(t *testing.T) {
	store := &fakeReaperStore{}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		auth.NewReaper(store, nil, 5*time.Millisecond, nil, nil).Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _ := store.calls(); n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the reaper never swept three times")
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

// TestReaperRunSurvivesAFailingSweep: a broken delete is logged, never
// fatal — the next tick retries.
func TestReaperRunSurvivesAFailingSweep(t *testing.T) {
	store := &fakeReaperStore{errCeremony: errors.New("delete failed")}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		auth.NewReaper(store, nil, time.Millisecond, nil, nil).Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _ := store.calls(); n >= 3 {
			break
		}
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
