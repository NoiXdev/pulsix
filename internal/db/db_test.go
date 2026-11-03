package db_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestMigrateCreatesTenancyTables(t *testing.T) {
	d := testdb.Open(t)
	for _, table := range []string{"organizations", "users", "org_memberships"} {
		var exists bool
		err := d.Pool().QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s missing", table)
		}
	}
}

// TestMigrateToStopsAtVersion runs on a database of its own: the shared one
// is already at the newest version, and MigrateTo never migrates down.
func TestMigrateToStopsAtVersion(t *testing.T) {
	ctx := context.Background()
	newest, err := testdb.Open(t).SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}

	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if v, err := d.SchemaVersion(ctx); err != nil || v != 0 {
		t.Fatalf("SchemaVersion before any migration = %d, %v; want 0, nil", v, err)
	}
	if err := d.MigrateTo(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != 20 {
		t.Fatalf("SchemaVersion after MigrateTo(20) = %d, %v; want 20, nil", v, err)
	}
	// 00020 adds jobs.attempts, 00021 adds host_connection_tests.host_public_key.
	var at20, at21 bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'attempts'),
		       EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'host_connection_tests' AND column_name = 'host_public_key')`,
	).Scan(&at20, &at21); err != nil {
		t.Fatal(err)
	}
	if !at20 || at21 {
		t.Fatalf("after MigrateTo(20): migration 20 applied = %v, migration 21 applied = %v; want true, false", at20, at21)
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != newest {
		t.Fatalf("SchemaVersion after Migrate = %d, %v; want %d, nil", v, err, newest)
	}
	if newest <= 20 {
		t.Fatalf("newest schema version %d is not past 20; the test proves nothing", newest)
	}
}

// TestCheckSchemaNotNewer: a database whose schema is ahead of this binary
// is refused with ErrSchemaNewer naming both versions; one at the latest
// version, one behind it and one with no schema at all pass.
func TestCheckSchemaNotNewer(t *testing.T) {
	ctx := context.Background()
	latest, err := db.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty database passes", func(t *testing.T) {
		d, err := db.Open(ctx, testdb.FreshDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.CheckSchemaNotNewer(ctx); err != nil {
			t.Fatalf("CheckSchemaNotNewer on an empty database: %v", err)
		}
	})

	t.Run("at the latest version passes", func(t *testing.T) {
		if err := testdb.Open(t).CheckSchemaNotNewer(ctx); err != nil {
			t.Fatalf("CheckSchemaNotNewer at version %d: %v", latest, err)
		}
	})

	t.Run("behind the latest version passes", func(t *testing.T) {
		d, err := db.Open(ctx, testdb.FreshDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.MigrateTo(ctx, 20); err != nil {
			t.Fatal(err)
		}
		if err := d.CheckSchemaNotNewer(ctx); err != nil {
			t.Fatalf("CheckSchemaNotNewer at version 20: %v", err)
		}
	})

	t.Run("ahead of the latest version is refused", func(t *testing.T) {
		d, err := db.Open(ctx, testdb.FreshDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		ahead := latest + 3
		if _, err := d.Pool().Exec(ctx,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, ahead); err != nil {
			t.Fatal(err)
		}
		err = d.CheckSchemaNotNewer(ctx)
		if !errors.Is(err, db.ErrSchemaNewer) {
			t.Fatalf("CheckSchemaNotNewer = %v, want ErrSchemaNewer", err)
		}
		for _, want := range []string{strconv.FormatInt(ahead, 10), strconv.FormatInt(latest, 10)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name version %s", err, want)
			}
		}
	})
}
