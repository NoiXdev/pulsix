package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// backfillBeginMarker and backfillEndMarker bound the exact backfill INSERT
// statement inside migration 00045_check_rules.sql (internal/db/migrations),
// so loadBackfillSQL can extract it verbatim rather than a hand-copied
// second draft of it living in this test file. Moving, renaming or
// deleting either marker without updating this constant — or vice versa —
// makes loadBackfillSQL fail loudly (see its own doc comment), which is the
// whole point: a copy that can silently drift is worse than no copy.
const (
	backfillBeginMarker = "-- rules-seed-backfill:begin"
	backfillEndMarker   = "-- rules-seed-backfill:end"
)

// loadBackfillSQL reads the real migration file off disk and returns
// exactly the statement between backfillBeginMarker and backfillEndMarker,
// so TestRulesBackfillMatchesSeedRules exercises the actual SQL a database
// runs when this migration applies — not a second, hand-maintained copy of
// it that could drift out of sync with the file the moment someone edits
// the migration without touching this test (the finding this test was
// rewritten to close). It fails the test outright, rather than returning
// an empty string that would let the caller silently insert zero rows, if
// the migration file cannot be read or either marker is missing.
func loadBackfillSQL(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("loadBackfillSQL: runtime.Caller could not resolve this test file's own path")
	}
	migrationPath := filepath.Join(filepath.Dir(thisFile), "..", "db", "migrations", "00045_check_rules.sql")
	contents, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("loadBackfillSQL: read %s: %v", migrationPath, err)
	}

	text := string(contents)
	beginIdx := strings.Index(text, backfillBeginMarker)
	if beginIdx == -1 {
		t.Fatalf("loadBackfillSQL: marker %q not found in %s — has it been renamed or removed?", backfillBeginMarker, migrationPath)
	}
	afterBegin := beginIdx + len(backfillBeginMarker)
	endIdx := strings.Index(text[afterBegin:], backfillEndMarker)
	if endIdx == -1 {
		t.Fatalf("loadBackfillSQL: marker %q not found after %q in %s — has it been renamed or removed?", backfillEndMarker, backfillBeginMarker, migrationPath)
	}

	stmt := strings.TrimSpace(text[afterBegin : afterBegin+endIdx])
	if stmt == "" {
		t.Fatalf("loadBackfillSQL: statement between the markers in %s is empty", migrationPath)
	}
	return stmt
}

// TestRulesBackfillMatchesSeedRules proves migration 00045's backfill
// INSERT: an organization that already has no check_rules rows (as every
// organization created before this migration's Task 5 addendum did) gets
// the same five-row starter set a newly created one gets from
// rules.Service.SeedForOrg. The statement under test is read straight out
// of the migration file (loadBackfillSQL) and executed against a real
// database — there is no way to re-run "only the backfill part" of an
// already-applied migration through goose, which is why this test
// re-executes the SQL directly rather than re-running the migration — so
// the only hand-kept-in-sync pair left is the migration's SQL and
// SeedRules() itself, exactly the 2-way sync the migration's own comment
// promises, never a 3-way one against a second copy living in this file.
//
// Unlike checks' own TestBackfillSeedsExistingOrganizations (which only
// checks keys and counts), this test decodes each backfilled row's ast/
// clear_ast back into a rules.Node tree and compares it, field for field,
// against what rules.SeedRules()'s own Form.Compile() produces for the
// same entry — proving the SQL and the Go function truly encode the same
// rule, not merely the same name and count.
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

	backfillSQL := loadBackfillSQL(t)
	// The migration's own statement backfills every organization in the
	// table (it runs once, for every pre-existing org, when the migration
	// applies) — no WHERE clause to scope it to just this test's org. That
	// is fine here: testdb.Open truncates every table after each test
	// (testdb.go's own t.Cleanup), so org is the only row in organizations
	// when this statement runs.
	if _, err := d.Pool().Exec(context.Background(), backfillSQL); err != nil {
		t.Fatalf("execute the migration's backfill statement: %v", err)
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
