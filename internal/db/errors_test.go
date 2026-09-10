package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestOpenRejectsInvalidURL(t *testing.T) {
	_, err := db.Open(context.Background(), "://not a url")
	if err == nil {
		t.Fatal("expected an error for a malformed connection URL")
	}
}

func TestOpenFailsWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Port 1 is a valid DSN but refuses connections, exercising the ping
	// failure path without needing a running container.
	_, err := db.Open(ctx, "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("expected a ping error for an unreachable host")
	}
}

func TestMigrateFailsWithCanceledContext(t *testing.T) {
	d := testdb.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Migrate(ctx); err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
}

// TestMigrateFailsWhenLockIsHeldByAnotherSession exercises the session-scoped
// advisory lock directly: it holds the lock on a connection Migrate does not
// control, so Migrate's own attempt to acquire it on its pinned connection
// blocks until the context times out. This is the behaviour the pinned-
// connection fix exists for — pg_advisory_lock/unlock only coordinate within
// one physical session, so contention has to be visible across sessions.
func TestMigrateFailsWhenLockIsHeldByAnotherSession(t *testing.T) {
	d := testdb.Open(t)

	holder, err := d.Pool().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	// 7256 mirrors the unexported db.migrationLockKey constant.
	if _, err := holder.Exec(context.Background(), `SELECT pg_advisory_lock(7256)`); err != nil {
		t.Fatal(err)
	}
	defer holder.Exec(context.Background(), `SELECT pg_advisory_unlock(7256)`) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = d.Migrate(ctx)
	if err == nil {
		t.Fatal("expected an error when the migration lock is already held by another session")
	}
	if !strings.Contains(err.Error(), "acquire migration lock") {
		t.Errorf("err = %v, want it to wrap the acquire-migration-lock step", err)
	}
}

func TestWithOrgFailsWithCanceledContext(t *testing.T) {
	d := testdb.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.WithOrg(ctx, uuid.New(), func(pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
}
