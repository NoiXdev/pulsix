package rules_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestRulesBackfillMatchesSeedRules proves migration 00045's backfill
// INSERT: an organization that already has no check_rules rows (as every
// organization created before this migration's Task 5 addendum did) gets
// the same five-row starter set a newly created one gets from
// rules.Service.SeedForOrg. This statement is copied from the migration's
// own Up section (they must stay in sync by hand — a migration cannot call
// Go code, and there is no way to re-run "only the backfill part" of an
// already-applied migration through goose, which is why this test
// re-executes the SQL directly rather than re-running the migration).
//
// Unlike checks' own TestBackfillSeedsExistingOrganizations (which only
// checks keys and counts), this test decodes each backfilled row's ast/
// clear_ast back into a rules.Node tree and compares it, field for field,
// against what rules.SeedRules()'s own Form.Compile() produces for the
// same entry — proving the SQL literal and the Go function truly encode
// the same rule, not merely the same name and count.
func TestRulesBackfillMatchesSeedRules(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-backfill")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	before, err := st.List(scoped, org, rules.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("before = %+v, want none", before)
	}

	const backfillSQL = `
INSERT INTO check_rules (
  id, org_id, name, enabled, origin, scope, check_key, metric, severity,
  mode, ast, clear_ast, source, for_duration
)
SELECT gen_random_uuid(), o.id, v.name, true, 'seed', 'org', v.check_key, v.metric, v.severity,
  'form', v.ast::jsonb, v.clear_ast::jsonb, v.source, v.for_duration::interval
FROM organizations o
CROSS JOIN (VALUES
  (
    'Disk usage warning', 'disk', 'disk.used_pct', 'warn',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":85}]}',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":80}]}',
    'disk.used_pct >= 85', '0'
  ),
  (
    'Disk usage critical', 'disk', 'disk.used_pct', 'crit',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":95}]}',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":90}]}',
    'disk.used_pct >= 95', '0'
  ),
  (
    'Memory usage warning', 'memory', 'mem.used_pct', 'warn',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"mem.used_pct"},{"kind":"number","num":90}]}',
    NULL,
    'mem.used_pct >= 90', '10 minutes'
  ),
  (
    'Memory usage critical', 'memory', 'mem.used_pct', 'crit',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"mem.used_pct"},{"kind":"number","num":97}]}',
    NULL,
    'mem.used_pct >= 97', '5 minutes'
  ),
  (
    'CPU usage warning', 'cpu', 'cpu.used_pct', 'warn',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"cpu.used_pct"},{"kind":"number","num":90}]}',
    NULL,
    'cpu.used_pct >= 90', '15 minutes'
  )
) AS v(name, check_key, metric, severity, ast, clear_ast, source, for_duration)
WHERE o.id = $1
`
	if _, err := d.Pool().Exec(context.Background(), backfillSQL, org); err != nil {
		t.Fatal(err)
	}

	after, err := st.List(scoped, org, rules.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := rules.SeedRules()
	if len(after) != len(want) {
		t.Fatalf("backfilled %d rows, want exactly %d (len(SeedRules()))", len(after), len(want))
	}

	byKey := map[string]rules.Rule{}
	for _, r := range after {
		byKey[r.Metric+"|"+string(r.Severity)] = r
	}
	for _, in := range want {
		key := in.Metric + "|" + string(in.Severity)
		got, ok := byKey[key]
		if !ok {
			t.Fatalf("backfill missing %s, got %+v", key, after)
		}
		wantTrigger, wantClear, cerr := in.Form.Compile()
		if cerr != nil {
			t.Fatalf("SeedRules() entry %q does not compile: %v", in.Name, cerr)
		}
		entry, ok := rules.Catalogue()[in.Metric]
		if !ok {
			t.Fatalf("SeedRules() entry %q names an unknown metric %q", in.Name, in.Metric)
		}

		if got.Origin != "seed" {
			t.Fatalf("backfilled %s origin = %q, want seed", key, got.Origin)
		}
		if got.Scope != in.Scope {
			t.Fatalf("backfilled %s scope = %q, want %q", key, got.Scope, in.Scope)
		}
		if got.Mode != "form" {
			t.Fatalf("backfilled %s mode = %q, want form", key, got.Mode)
		}
		if got.CheckKey != entry.CheckKey {
			t.Fatalf("backfilled %s check_key = %q, want %q", key, got.CheckKey, entry.CheckKey)
		}
		if got.For != in.For {
			t.Fatalf("backfilled %s for_duration = %s, want %s", key, got.For, in.For)
		}
		if !reflect.DeepEqual(got.Trigger, wantTrigger) {
			t.Fatalf("backfilled %s trigger = %+v, want %+v (from SeedRules())", key, got.Trigger, wantTrigger)
		}
		if !reflect.DeepEqual(got.Clear, wantClear) {
			t.Fatalf("backfilled %s clear = %+v, want %+v (from SeedRules())", key, got.Clear, wantClear)
		}
	}
}
