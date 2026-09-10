package testdb_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestOpenReturnsMigratedDB(t *testing.T) {
	d := testdb.Open(t)
	if err := d.Pool().Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestOpenTruncatesTablesBetweenTests(t *testing.T) {
	d := testdb.Open(t)

	var count int
	err := d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM organizations`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("organizations has %d rows, want 0 (truncated between tests)", count)
	}

	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO organizations (id, slug, name) VALUES (gen_random_uuid(), 'truncate-test', 'Truncate Test')`); err != nil {
		t.Fatal(err)
	}
}
