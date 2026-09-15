// The advisory locker and the default-value fallbacks, driven against the
// real database (the lock lives on a session, so only a real pool shows it).
package schedule_test

import (
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
