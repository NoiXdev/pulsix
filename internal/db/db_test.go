package db_test

import (
	"context"
	"testing"

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
