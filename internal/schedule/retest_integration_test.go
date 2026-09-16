// The advisory locker and the default-value fallbacks, driven against the
// real database (the lock lives on a session, so only a real pool shows it).
package schedule_test

import (
	"context"
	"testing"

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
