package testdb_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestOpenReturnsMigratedDB(t *testing.T) {
	d := testdb.Open(t)
	if err := d.Pool().Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// TestURLReturnsMigratedConnectionString covers URL directly (rather than
// only through its actual consumers, internal/app and internal/setup, whose
// own coverage numbers do not count toward this package's): the string it
// returns must open a connection that has already been migrated, and — the
// property internal/app's own RLS-guard test relies on — that connection
// must be the bootstrap superuser, not pulsix_app.
func TestURLReturnsMigratedConnectionString(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)

	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var exists bool
	if err := d.Pool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'organizations')`,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("URL's connection string is not already migrated")
	}

	var isSuperuser bool
	if err := d.Pool().QueryRow(ctx,
		`SELECT rolsuper FROM pg_roles WHERE rolname = current_user`,
	).Scan(&isSuperuser); err != nil {
		t.Fatal(err)
	}
	if !isSuperuser {
		t.Fatal("URL's connection string must authenticate as the bootstrap superuser, not pulsix_app")
	}
}

// TestTruncateCoversTablesNotInAnyHandWrittenList proves truncateAll's
// coverage still comes from querying pg_tables at truncate time, not from a
// list anyone edited by hand: a table this test creates on the fly, which no
// migration and no line in testdb.go ever names, must still come back empty
// in the next test. If truncateAll ever regressed to a fixed table list, this
// is the case that would catch it — a real migration adding a table would
// otherwise rot that list silently until a seemingly unrelated test flaked.
func TestTruncateCoversTablesNotInAnyHandWrittenList(t *testing.T) {
	t.Run("create an ad hoc table and insert a row", func(t *testing.T) {
		url := testdb.URL(t)
		ctx := context.Background()
		admin, err := db.Open(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		if _, err := admin.Pool().Exec(ctx,
			`CREATE TABLE IF NOT EXISTS zz_testdb_coverage_probe (id int)`); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Pool().Exec(ctx,
			`INSERT INTO zz_testdb_coverage_probe (id) VALUES (1)`); err != nil {
			t.Fatal(err)
		}
	})

	// This subtest depends on the previous one having run first in the same
	// test binary (truncate empties rows, it does not drop the table, so
	// the ordering dependency is "the table already exists", not "it has a
	// row in it"). Run alone — e.g. via -run of just this subtest — its own
	// SELECT fails with "relation does not exist" rather than a clear
	// assertion failure; that is accepted here rather than engineered away,
	// since go test always runs a parent test's subtests in the order
	// they're registered.
	t.Run("table is empty after the subtest's cleanup", func(t *testing.T) {
		url := testdb.URL(t)
		ctx := context.Background()
		d, err := db.Open(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()

		var count int
		if err := d.Pool().QueryRow(ctx,
			`SELECT count(*) FROM zz_testdb_coverage_probe`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("zz_testdb_coverage_probe has %d rows, want 0 (truncateAll must cover tables no list names by hand)", count)
		}

		// Drop the probe table rather than leaving it in public for the
		// rest of the binary: every later truncateAll call would otherwise
		// keep including it in its table list for no reason.
		if _, err := d.Pool().Exec(ctx, `DROP TABLE IF EXISTS zz_testdb_coverage_probe`); err != nil {
			t.Errorf("drop coverage probe table: %v", err)
		}
	})
}

func TestOpenTruncatesTablesBetweenTests(t *testing.T) {
	// Two subtests sharing one testdb.Open each: the first subtest's
	// t.Cleanup (which truncates) runs when it returns, before the second
	// subtest opens its own DB and reads. That makes the second subtest's
	// "table is empty" assertion actually depend on truncation having run —
	// disabling the truncate in testdb.go must fail this test.
	t.Run("insert a row", func(t *testing.T) {
		d := testdb.Open(t)
		if _, err := d.Pool().Exec(context.Background(),
			`INSERT INTO organizations (id, slug, name) VALUES (gen_random_uuid(), 'truncate-test', 'Truncate Test')`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("table is empty again", func(t *testing.T) {
		d := testdb.Open(t)

		var count int
		err := d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM organizations`).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("organizations has %d rows, want 0 (truncated between tests)", count)
		}
	})
}
