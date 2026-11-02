package db_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestLatestVersionIsTheMigratedVersion: the newest embedded migration is
// the version a fully migrated database reports.
func TestLatestVersionIsTheMigratedVersion(t *testing.T) {
	latest, err := db.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest < 75 {
		t.Fatalf("LatestVersion = %d; backup_runs alone is migration 75", latest)
	}
	d, err := db.Open(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := d.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != latest {
		t.Errorf("a migrated database is at %d, LatestVersion says %d", got, latest)
	}
}
