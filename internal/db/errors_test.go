package db_test

import (
	"context"
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

func TestWithOrgFailsWithCanceledContext(t *testing.T) {
	d := testdb.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.WithOrg(ctx, uuid.New(), func(pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
}
