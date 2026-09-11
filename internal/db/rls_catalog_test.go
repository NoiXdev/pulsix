package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestEveryOrgOwnedTableForcesRowLevelSecurity(t *testing.T) {
	d := testdb.Open(t)
	rows, err := d.Pool().Query(context.Background(), `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       COALESCE((SELECT qual FROM pg_policies p WHERE p.tablename = c.relname AND p.policyname = 'org_isolation'), ''),
		       EXISTS (SELECT 1 FROM pg_policies p WHERE p.tablename = c.relname AND p.policyname = 'org_isolation_bypass')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relkind = 'r'
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped)
		ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, qual string
		var rls, force, bypass bool
		if err := rows.Scan(&name, &rls, &force, &qual, &bypass); err != nil {
			t.Fatal(err)
		}
		seen++
		if !rls || !force {
			t.Errorf("%s: row level security enabled=%v forced=%v", name, rls, force)
		}
		if !strings.Contains(qual, "NULLIF") {
			t.Errorf("%s: org_isolation policy missing or not in its NULLIF form: %q", name, qual)
		}
		if !bypass {
			t.Errorf("%s: org_isolation_bypass policy missing", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen < 7 {
		t.Fatalf("found only %d org-owned tables; the catalog query is wrong", seen)
	}
}
