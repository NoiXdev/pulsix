// Package testdb starts one TimescaleDB container per test binary.
package testdb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/noixdev/pulsix/internal/db"
)

var (
	once sync.Once
	url  string
	errC error
)

func start() {
	ctx := context.Background()
	c, err := postgres.Run(ctx, "timescale/timescaledb:2.30.0-pg18",
		postgres.WithDatabase("pulsix"), postgres.WithUsername("pulsix"), postgres.WithPassword("pulsix"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)))
	if err != nil {
		errC = err
		return
	}
	url, errC = c.ConnectionString(ctx, "sslmode=disable")
}

// Open returns a migrated DB. All tables are truncated when the test ends.
func Open(t *testing.T) *db.DB {
	t.Helper()
	once.Do(start)
	fatalOnErr(t, errC)

	ctx := context.Background()
	d, err := db.Open(ctx, url)
	fatalOnErr(t, err)

	err = d.Migrate(ctx)
	fatalOnErr(t, err)

	t.Cleanup(func() {
		_, _ = d.Pool().Exec(ctx, `DO $$ DECLARE r record; BEGIN
			FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' LOOP
				EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.tablename) || ' CASCADE';
			END LOOP; END $$;`)
		d.Close()
	})
	return d
}

// fataler is the subset of *testing.T that fatalOnErr needs. It exists so
// the failure branch can be exercised by a plain fake in fatal_test.go
// instead of a real *testing.T, whose Fatal aborts the calling goroutine.
type fataler interface {
	Helper()
	Fatal(args ...any)
}

// fatalOnErr fails the test immediately if err is non-nil. Shared by every
// setup step in Open so the failure path is a single, directly testable
// statement instead of three duplicated inline checks.
func fatalOnErr(t fataler, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
