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
