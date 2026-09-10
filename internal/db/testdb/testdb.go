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

// appRole is the least-privilege PostgreSQL role the returned *db.DB queries
// through, matching how the manager connects in production. Using it here
// instead of the container's bootstrap superuser is what makes row-level
// security policies actually apply: superusers and the schema owner are
// exempt from RLS regardless of FORCE ROW LEVEL SECURITY.
const appRole = "pulsix_app"

// Open returns a migrated DB, connected as the pulsix_app role so that
// row-level security policies apply to it. All tables are truncated when the
// test ends.
func Open(t *testing.T) *db.DB {
	t.Helper()
	once.Do(start)
	fatalOnErr(t, errC)

	ctx := context.Background()

	// Migrations need DDL privileges (creating tables, extensions and the
	// pulsix_app role itself), so they run through the bootstrap superuser
	// connection. That connection is kept open only for the post-test
	// truncate, since pulsix_app is deliberately not granted TRUNCATE.
	admin, err := db.Open(ctx, url)
	fatalOnErr(t, err)

	err = admin.Migrate(ctx)
	fatalOnErr(t, err)

	d, err := db.OpenAsRole(ctx, url, appRole)
	fatalOnErr(t, err)

	t.Cleanup(func() {
		// A failing truncate must be loud: silently leaving rows behind makes
		// the next test fail somewhere unrelated.
		if _, err := admin.Pool().Exec(ctx, `DO $$ DECLARE r record; BEGIN
			FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' LOOP
				EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.tablename) || ' CASCADE';
			END LOOP; END $$;`); err != nil {
			t.Errorf("truncate tables after test: %v", err)
		}
		d.Close()
		admin.Close()
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
