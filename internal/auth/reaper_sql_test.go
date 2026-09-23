// The reaper's SQL pass, driven against the real database: the boundary
// rows pin the strictness of both expiry predicates, the advisory lock
// keeps a concurrent manager's sweep out, and live rows survive untouched.
package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/schedule"
)

// baseTime is the sweep clock's instant, exact to the second so the
// boundary rows (baseTime, baseTime-7d, baseTime-7d-1µs) survive the
// timestamptz round-trip unchanged.
var baseTime = time.Unix(1788763200, 0).UTC()

// mutableClock is the injected clock of a reaper under test; Advance moves
// it past an expiry boundary between sweeps.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMutableClock(t time.Time) *mutableClock { return &mutableClock{now: t} }

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func seedReaperUser(t *testing.T, q *gen.Queries, email string) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: email, DisplayName: email}); err != nil {
		t.Fatal(err)
	}
	return uid
}

func putCeremonyAt(t *testing.T, q *gen.Queries, expiresAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := q.PutCeremony(ctx, gen.PutCeremonyParams{
		ID: id, Kind: "login", Data: []byte(`{"challenge":"x"}`), ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func createSessionAt(t *testing.T, q *gen.Queries, userID uuid.UUID, expiresAt time.Time, revoked bool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := q.CreateSession(ctx, gen.CreateSessionParams{
		ID: id, UserID: userID, SecretHash: []byte("hash"), MfaComplete: false, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	if revoked {
		if err := q.RevokeSession(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func countCeremonies(t *testing.T, d *db.DB, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM webauthn_ceremonies WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countSessions(t *testing.T, d *db.DB, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReaperSQLReapsExpiredRowsSparesLive drives the real DELETE queries:
// an expired ceremony and the sessions expired beyond the 7-day grace go,
// live rows and rows inside the grace window stay. The boundary rows (a
// ceremony exactly at its expiry, a session exactly 7 days past its
// expiry) survive the first sweep — both predicates are strict, a row is
// deleted once its threshold is exceeded — and go on the next sweep one
// microsecond later.
func TestReaperSQLReapsExpiredRowsSparesLive(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	clock := newMutableClock(baseTime)
	reaper := auth.NewReaper(q, nil, 0, clock.Now, nil)

	// Live rows: a fresh ceremony (exactly as the passkey service writes
	// it: begin + CeremonyTTL) and a valid session.
	liveCeremony := putCeremonyAt(t, q, baseTime.Add(auth.CeremonyTTL))
	u1 := seedReaperUser(t, q, "reaper-live@example.test")
	u2 := seedReaperUser(t, q, "reaper-live2@example.test")
	liveSession := createSessionAt(t, q, u1, baseTime.Add(time.Hour), false)

	// Expired rows past the threshold: one ceremony just past its expiry,
	// sessions just past the 7-day grace, on both users.
	goneCeremony := putCeremonyAt(t, q, baseTime.Add(-time.Microsecond))
	goneSession := createSessionAt(t, q, u1, baseTime.Add(-7*24*time.Hour-time.Microsecond), false)
	goneSession2 := createSessionAt(t, q, u2, baseTime.Add(-7*24*time.Hour-time.Microsecond), false)

	// Boundary and grace rows that must survive this sweep.
	boundaryCeremony := putCeremonyAt(t, q, baseTime) // expired exactly at cutoff
	withinGrace := createSessionAt(t, q, u1, baseTime.Add(-time.Hour), false)
	boundarySession := createSessionAt(t, q, u1, baseTime.Add(-7*24*time.Hour), false)
	revokedWithinGrace := createSessionAt(t, q, u1, baseTime.Add(-time.Hour), true)

	ceremonies, sessions, err := reaper.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if ceremonies != 1 || sessions != 2 {
		t.Fatalf("Sweep = %d ceremonies / %d sessions, want 1/2", ceremonies, sessions)
	}
	if countCeremonies(t, d, goneCeremony) != 0 {
		t.Fatal("the expired ceremony must be deleted")
	}
	if countCeremonies(t, d, liveCeremony) != 1 {
		t.Fatal("the live ceremony must survive")
	}
	if countCeremonies(t, d, boundaryCeremony) != 1 {
		t.Fatal("a ceremony expired exactly at the cutoff must survive a strict-less-than sweep")
	}
	if countSessions(t, d, goneSession) != 0 || countSessions(t, d, goneSession2) != 0 {
		t.Fatal("the sessions beyond the grace window must be deleted")
	}
	for _, id := range []uuid.UUID{liveSession, withinGrace, boundarySession, revokedWithinGrace} {
		if countSessions(t, d, id) != 1 {
			t.Fatalf("session %s must survive the sweep", id)
		}
	}

	// One microsecond past the boundary: the exactly-at-threshold rows are
	// now beyond their thresholds and go.
	clock.Advance(time.Microsecond)
	ceremonies, sessions, err = reaper.Sweep(ctx)
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if ceremonies != 1 || sessions != 1 {
		t.Fatalf("second Sweep = %d ceremonies / %d sessions, want 1/1", ceremonies, sessions)
	}
	if countCeremonies(t, d, boundaryCeremony) != 0 {
		t.Fatal("the boundary ceremony must go once its expiry is past")
	}
	if countSessions(t, d, boundarySession) != 0 {
		t.Fatal("a session expired by more than 7 days must be deleted")
	}
	if countCeremonies(t, d, liveCeremony) != 1 {
		t.Fatal("the live ceremony must survive the second sweep too")
	}
	for _, id := range []uuid.UUID{liveSession, withinGrace, revokedWithinGrace} {
		if countSessions(t, d, id) != 1 {
			t.Fatalf("session %s must survive the second sweep", id)
		}
	}
}

// blockingReaperStore wraps the real queries: its first delete call
// signals and then blocks on the gate, so the test can hold the reaper's
// advisory lock while a second manager's sweep races it.
type blockingReaperStore struct {
	inner   *gen.Queries
	entered chan struct{}
	gate    chan struct{}
}

func (s *blockingReaperStore) DeleteExpiredCeremonies(inner context.Context, expiredBefore time.Time) (int64, error) {
	s.entered <- struct{}{}
	<-s.gate
	return s.inner.DeleteExpiredCeremonies(inner, expiredBefore)
}

func (s *blockingReaperStore) DeleteExpiredSessions(inner context.Context, expiredBefore time.Time) (int64, error) {
	return s.inner.DeleteExpiredSessions(inner, expiredBefore)
}

func heldAdvisoryLocks(t *testing.T, d *db.DB, key int64) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReaperSQLAdvisoryLockPreventsConcurrentDoubleRun: while one manager
// instance's sweep holds the advisory lock, a second instance's sweep (same
// pool, same key) skips instead of deleting the same rows again — and the
// lock is released when the first sweep ends.
func TestReaperSQLAdvisoryLockPreventsConcurrentDoubleRun(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	clock := newMutableClock(baseTime)

	// An expired ceremony and a session beyond the grace window: whatever
	// runs exactly once must remove both.
	u := seedReaperUser(t, q, "reaper-lock@example.test")
	goneCeremony := putCeremonyAt(t, q, baseTime.Add(-time.Microsecond))
	goneSession := createSessionAt(t, q, u, baseTime.Add(-7*24*time.Hour-time.Microsecond), false)

	first := &blockingReaperStore{inner: q, entered: make(chan struct{}), gate: make(chan struct{})}
	locker1 := schedule.NewAdvisoryLocker(d.Pool(), auth.AdvisoryKeyAuthReaper)
	r1 := auth.NewReaper(first, locker1, 0, clock.Now, nil)
	type sweepResult struct {
		ceremonies, sessions int64
		err                  error
	}
	result := make(chan sweepResult, 1)
	go func() {
		ceremonies, sessions, err := r1.Sweep(ctx)
		result <- sweepResult{ceremonies, sessions, err}
	}()
	<-first.entered

	// The first sweep holds exactly one advisory lock on the reaper's key.
	if n := heldAdvisoryLocks(t, d, auth.AdvisoryKeyAuthReaper); n != 1 {
		t.Fatalf("%d sessions hold the advisory lock mid-sweep, want exactly one", n)
	}

	// The second manager's sweep loses the race and does nothing.
	second := &fakeReaperStore{}
	r2 := auth.NewReaper(second, schedule.NewAdvisoryLocker(d.Pool(), auth.AdvisoryKeyAuthReaper), 0, clock.Now, nil)
	ceremonies, sessions, err := r2.Sweep(ctx)
	if err != nil {
		t.Fatalf("second reaper's Sweep: %v", err)
	}
	if ceremonies != 0 || sessions != 0 {
		t.Fatalf("second reaper deleted %d/%d rows, want a clean skip", ceremonies, sessions)
	}
	if n, _ := second.calls(); n != 0 {
		t.Fatal("the skipped reaper must not touch the store")
	}
	if countCeremonies(t, d, goneCeremony) != 1 || countSessions(t, d, goneSession) != 1 {
		t.Fatal("the blocked sweep's rows must still be there while it holds the lock")
	}

	// Let the first sweep finish: it removes both rows and releases the
	// lock.
	close(first.gate)
	got := <-result
	if got.err != nil {
		t.Fatalf("first reaper's Sweep: %v", got.err)
	}
	if got.ceremonies != 1 || got.sessions != 1 {
		t.Fatalf("first reaper deleted %d/%d rows, want 1/1", got.ceremonies, got.sessions)
	}
	if countCeremonies(t, d, goneCeremony) != 0 || countSessions(t, d, goneSession) != 0 {
		t.Fatal("the first sweep's deletes must land")
	}
	locker1.Unlock(ctx)
	if n := heldAdvisoryLocks(t, d, auth.AdvisoryKeyAuthReaper); n != 0 {
		t.Fatalf("%d sessions still hold the advisory lock after the sweep", n)
	}
}
