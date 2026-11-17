package backup_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestSQLStoreTakesTheLockSessionFromItsLockPool (backlog 3.92): the
// backup lock's session comes from the lock pool, so a held lock leaves
// every connection of the pool the backup itself runs on free.
func TestSQLStoreTakesTheLockSessionFromItsLockPool(t *testing.T) {
	app := testdb.Open(t)
	locks := testdb.Open(t)
	ctx := context.Background()

	release, ok, err := backup.NewSQLStoreWithLocks(app, locks).TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("TryLock = %v %v", ok, err)
	}
	if n := locks.Pool().Stat().AcquiredConns(); n != 1 {
		t.Errorf("lock pool acquired connections while locked = %d, want 1", n)
	}
	if n := app.Pool().Stat().AcquiredConns(); n != 0 {
		t.Errorf("app pool acquired connections while locked = %d, want 0", n)
	}
	release()
	if n := locks.Pool().Stat().AcquiredConns(); n != 0 {
		t.Errorf("lock pool acquired connections after release = %d, want 0", n)
	}
}
