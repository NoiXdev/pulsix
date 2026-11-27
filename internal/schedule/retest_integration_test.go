// The advisory locker and the default-value fallbacks, driven against the
// real database (the lock lives on a session, so only a real pool shows it).
package schedule_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/schedule"
)

func TestAdvisoryLockerExcludesConcurrentHolders(t *testing.T) {
	d := testdb.Open(t)
	key := schedule.AdvisoryKeyRetest

	first := schedule.NewAdvisoryLocker(d.Pool(), key)
	locked, err := first.TryLock(ctx)
	if err != nil || !locked {
		t.Fatalf("TryLock = %v, %v", locked, err)
	}

	// A second locker over the same key loses the race.
	second := schedule.NewAdvisoryLocker(d.Pool(), key)
	locked, err = second.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Fatal("a second holder must lose the advisory lock race")
	}

	// Unlock releases it; the second locker then wins.
	first.Unlock(ctx)
	locked, err = second.TryLock(ctx)
	if err != nil || !locked {
		t.Fatalf("after release: TryLock = %v, %v", locked, err)
	}
	second.Unlock(ctx)

	// Unlocking twice is a no-op, not a panic.
	second.Unlock(ctx)
}

func TestAdvisoryLockerUnlockWithoutHoldIsNoop(t *testing.T) {
	d := testdb.Open(t)
	locker := schedule.NewAdvisoryLocker(d.Pool(), 4242)
	locker.Unlock(ctx) // nothing held: must not panic or error
}

// TestAdvisoryLockerUnlockSurvivesACancelledContext: the scheduler unlocks
// with its tick context, which shutdown cancels. The unlock must still run —
// otherwise the pooled session goes back to the pool holding the lock and
// every other manager instance (and this one after a restart of the loop)
// is locked out until that session happens to close.
func TestAdvisoryLockerUnlockSurvivesACancelledContext(t *testing.T) {
	d := testdb.Open(t)
	key := int64(4343)
	first := schedule.NewAdvisoryLocker(d.Pool(), key)
	if locked, err := first.TryLock(ctx); err != nil || !locked {
		t.Fatalf("TryLock = %v, %v", locked, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	first.Unlock(cancelled)

	// pg_locks shows every session's locks: a session that went back to
	// the pool still holding the advisory lock is visible here (a second
	// TryLock could be handed that very session and re-enter the lock).
	var held int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted`, key).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("after an unlock with a cancelled context %d session(s) still hold the advisory lock", held)
	}
}

// TestAdvisoryLockerNoticesALostSession: a holder whose session died (a
// terminated backend, a dropped connection) no longer holds the lock, and
// another instance may already hold it. The holder's next TryLock must
// say so rather than answer true from its cached connection — otherwise
// two instances run the loop the lock exists to keep single.
func TestAdvisoryLockerNoticesALostSession(t *testing.T) {
	d := testdb.Open(t)
	key := int64(4444)
	a := schedule.NewAdvisoryLocker(d.Pool(), key)
	if locked, err := a.TryLock(ctx); err != nil || !locked {
		t.Fatalf("A: TryLock = %v, %v", locked, err)
	}
	// Releases A's session whatever happens below: a pooled connection
	// never returned would hang the pool's Close at cleanup.
	defer a.Unlock(ctx)
	// Still holding: a second TryLock on a healthy session stays true.
	if locked, err := a.TryLock(ctx); err != nil || !locked {
		t.Fatalf("A again: TryLock = %v, %v", locked, err)
	}

	var pid int32
	if err := d.Pool().QueryRow(ctx,
		`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted`, key).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	// Only the bootstrap superuser may terminate another role's backend.
	admin, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var terminated bool
	if err := admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("pg_terminate_backend = %v, %v", terminated, err)
	}

	b := schedule.NewAdvisoryLocker(d.Pool(), key)
	deadline := time.Now().Add(10 * time.Second)
	for {
		locked, err := b.TryLock(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B never acquired the lock after A's session was terminated")
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer b.Unlock(ctx)

	locked, err := a.TryLock(ctx)
	if err != nil {
		t.Fatalf("A after losing its session: %v", err)
	}
	if locked {
		t.Fatal("A's TryLock answered true from a dead session while B holds the lock")
	}
	a.Unlock(ctx) // holds nothing: a no-op
}

// TestAdvisoryLockerGenerationCountsSessions (backlog 3.79 review): the
// generation changes exactly when TryLock answers true on a new session —
// the first acquire, a re-acquire after Unlock, and a re-acquire after the
// held session died — and stays while the same session keeps the lock, so
// a loop holding the lock across ticks can tell "still held" from "lost
// and taken again in between".
func TestAdvisoryLockerGenerationCountsSessions(t *testing.T) {
	d := testdb.Open(t)
	key := int64(4545)
	a := schedule.NewAdvisoryLocker(d.Pool(), key)
	defer a.Unlock(ctx)
	if g := a.Generation(); g != 0 {
		t.Fatalf("generation before any lock = %d, want 0", g)
	}
	tryLock := func(want uint64) {
		t.Helper()
		if locked, err := a.TryLock(ctx); err != nil || !locked {
			t.Fatalf("TryLock = %v, %v", locked, err)
		}
		if g := a.Generation(); g != want {
			t.Fatalf("generation = %d, want %d", g, want)
		}
	}
	tryLock(1)
	tryLock(1) // the same session still holds it
	a.Unlock(ctx)
	tryLock(2)

	var pid int32
	if err := d.Pool().QueryRow(ctx,
		`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted`, key).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var terminated bool
	if err := admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("pg_terminate_backend = %v, %v", terminated, err)
	}
	// The dead session is noticed and the lock, free now, taken on a new one.
	deadline := time.Now().Add(10 * time.Second)
	for {
		locked, err := a.TryLock(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("A never re-acquired the lock after its session was terminated")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if g := a.Generation(); g != 3 {
		t.Fatalf("generation after re-acquiring on a new session = %d, want 3", g)
	}
}
