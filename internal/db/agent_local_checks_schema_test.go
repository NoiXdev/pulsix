package db_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestHostAgentsLocalCheckColumns: migration 00095 adds the agent's
// reported local checks and their problems to host_agents (backlog 3.5a),
// both nullable with no default — NULL is "unknown" (an agent older than
// local checks, or one that has not reported since it enrolled), which
// existing rows read as.
func TestHostAgentsLocalCheckColumns(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	want := map[string]string{"local_checks": "jsonb", "local_check_problems": "ARRAY"}
	rows, err := d.Pool().Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default IS NULL
		FROM information_schema.columns
		WHERE table_name = 'host_agents' AND column_name IN ('local_checks', 'local_check_problems')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, typ, nullable string
		var noDefault bool
		if err := rows.Scan(&name, &typ, &nullable, &noDefault); err != nil {
			t.Fatal(err)
		}
		seen++
		if want[name] != typ || nullable != "YES" || !noDefault {
			t.Errorf("%s: type %s nullable %s no-default %v; want %s, nullable, no default", name, typ, nullable, noDefault, want[name])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("found %d of the two local check columns", seen)
	}
	var elem string
	if err := d.Pool().QueryRow(ctx, `
		SELECT e.data_type FROM information_schema.columns c
		JOIN information_schema.element_types e
		  ON e.object_schema = c.table_schema AND e.object_name = c.table_name
		 AND e.object_type = 'TABLE' AND e.collection_type_identifier = c.dtd_identifier
		WHERE c.table_name = 'host_agents' AND c.column_name = 'local_check_problems'`).Scan(&elem); err != nil {
		t.Fatal(err)
	}
	if elem != "text" {
		t.Fatalf("local_check_problems element type = %s, want text", elem)
	}
}
