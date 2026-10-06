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
	if errC != nil {
		return
	}

	// Migrate exactly once per test binary, here inside the same sync.Once
	// that starts the container, instead of once per call to Open or URL.
	// goose's own up-to-date check ("no migrations to run") still means
	// reading its bookkeeping table and walking the embedded migration list
	// on every single call, and with Open called dozens of times in one
	// package that added up to real per-test cost for no benefit: nothing
	// changes the schema between one test and the next (backlog 3.46).
	admin, err := db.Open(ctx, url)
	if err != nil {
		errC = err
		return
	}
	defer admin.Close()
	errC = admin.Migrate(ctx)
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

	// Migrations already ran once in start(), through the bootstrap
	// superuser, before url was ever handed out — see the comment there.
	// This connection exists only to run the post-test truncate, since
	// pulsix_app is deliberately not granted TRUNCATE.
	admin, err := db.Open(ctx, url)
	fatalOnErr(t, err)

	d, err := db.OpenAsRole(ctx, url, appRole)
	fatalOnErr(t, err)

	t.Cleanup(func() {
		truncateAll(ctx, t, admin)
		d.Close()
		admin.Close()
	})
	return d
}

// URL returns the raw connection string for the shared test database,
// already migrated, for tests that need to open their own *db.DB against a
// role Open's own pulsix_app-scoped *db.DB cannot express — namely
// internal/app's two-pool wiring test and the negative test asserting
// app.Build refuses to start when connected as a role that can bypass row-
// level security (Open always connects as pulsix_app, which by design
// never can). The connection this string opens is the bootstrap superuser
// Open uses internally for migrations, not pulsix_app.
//
// All tables are truncated when the test ends, the same as Open — a caller
// of URL is expected to open and close its own *db.DB(s) against it during
// the test, not hold one past t's cleanup.
func URL(t *testing.T) string {
	t.Helper()
	once.Do(start)
	fatalOnErr(t, errC)

	ctx := context.Background()
	// Already migrated by start(); this connection exists only for the
	// post-test truncate below.
	admin, err := db.Open(ctx, url)
	fatalOnErr(t, err)
	t.Cleanup(func() {
		truncateAll(ctx, t, admin)
		admin.Close()
	})
	return url
}

// truncateAll empties every table (except goose's own bookkeeping table)
// through admin, the bootstrap superuser connection — pulsix_app is
// deliberately not granted TRUNCATE. Shared by Open and URL's cleanup.
func truncateAll(ctx context.Context, t *testing.T, admin *db.DB) {
	t.Helper()
	// One multi-table TRUNCATE instead of one statement per table: Postgres
	// takes every table's lock together instead of one at a time, which
	// measured dramatically cheaper once the schema grew past a few dozen
	// tables, several of them TimescaleDB hypertables (backlog 3.46). The
	// table list is still built from pg_tables inside the same DO block, at
	// truncate time, so a migration that adds a table tomorrow is still
	// covered automatically — nothing here names a table by hand.
	//
	// A failing truncate must be loud: silently leaving rows behind makes
	// the next test fail somewhere unrelated.
	if _, err := admin.Pool().Exec(ctx, `DO $$
	DECLARE tables text;
	BEGIN
		SELECT string_agg(quote_ident(tablename), ', ') INTO tables
			FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version';
		IF tables IS NOT NULL THEN
			EXECUTE 'TRUNCATE TABLE ' || tables || ' CASCADE';
		END IF;
	END $$;`); err != nil {
		t.Errorf("truncate tables after test: %v", err)
	}
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
