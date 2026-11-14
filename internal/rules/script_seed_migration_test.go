package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// scriptRuleNames are the two seed rules migration 00094 backfills.
var scriptRuleNames = []string{"Script reports critical", "Script reports warning"}

// namedRuleRows returns org's check_rules rows with one of names, each as
// the whole row's jsonb without the columns that differ between any two
// writes of the same rule (ids, organization, timestamps), keyed by name;
// a name held twice fails the test.
func namedRuleRows(ctx context.Context, t *testing.T, d *db.DB, org uuid.UUID, names []string) map[string]string {
	t.Helper()
	rows, err := d.Pool().Query(ctx, `
SELECT name, (to_jsonb(r) - 'id' - 'org_id' - 'created_at' - 'updated_at')::text
FROM check_rules r WHERE org_id = $1 AND name = ANY($2)`, org, names)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, row string
		if err := rows.Scan(&name, &row); err != nil {
			t.Fatal(err)
		}
		if _, dup := got[name]; dup {
			t.Fatalf("organization %s holds two rules named %q", org, name)
		}
		got[name] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// scriptRuleRows is namedRuleRows for the script seed rules.
func scriptRuleRows(ctx context.Context, t *testing.T, d *db.DB, org uuid.UUID) map[string]string {
	t.Helper()
	return namedRuleRows(ctx, t, d, org, scriptRuleNames)
}

// TestScriptRulesMigrationMatchesSeedForOrg (backlog 3.6g): migration 00094
// writes the two script seed rules for an organization that existed before
// it, SeedForOrg writes them for one created afterwards, and the two must
// agree on every column but the ids, organization and timestamps — the
// migration encodes SeedRules() by hand, so this is what notices the two
// drifting apart. An organization already holding a rule of either name
// keeps it and gets no second one; the Down removes only the seed rows.
func TestScriptRulesMigrationMatchesSeedForOrg(t *testing.T) {
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 93); err != nil {
		t.Fatal(err)
	}
	plain, taken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'plain', 'plain'), ($2, 'taken', 'taken')`, []any{plain, taken}},
		// The operator of "taken" already wrote a rule under one of the
		// names, on another metric even, and it must survive alone.
		{`INSERT INTO check_rules (id, org_id, name, origin, scope, check_key, metric, severity, mode, ast, source)
		  VALUES (gen_random_uuid(), $1, 'Script reports warning', 'user', 'org', 'cpu', 'cpu.used_pct', 'warn', 'expression',
		  '{"kind":"compare","op":">","args":[{"kind":"metric","name":"cpu.used_pct"},{"kind":"number","num":50}]}', 'cpu.used_pct > 50')`, []any{taken}},
	} {
		if _, err := d.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%.40s: %v", stmt.sql, err)
		}
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	created := seedOrg(t, d, "created")
	if err := rules.NewService(rules.NewSQLStore(d)).SeedForOrg(db.WithOrgScope(ctx, created), created); err != nil {
		t.Fatal(err)
	}
	fromSeed := scriptRuleRows(ctx, t, d, created)
	if len(fromSeed) != len(scriptRuleNames) {
		t.Fatalf("SeedForOrg wrote %v, want %v", fromSeed, scriptRuleNames)
	}

	operatorRow := scriptRuleRows(ctx, t, d, taken)["Script reports warning"]
	assertAfterUp := func(t *testing.T) {
		t.Helper()
		fromMigration := scriptRuleRows(ctx, t, d, plain)
		for name, want := range fromSeed {
			if fromMigration[name] != want {
				t.Errorf("%s from migration 00094 = %s\nwant SeedForOrg's %s", name, fromMigration[name], want)
			}
		}
		gotTaken := scriptRuleRows(ctx, t, d, taken)
		if gotTaken["Script reports critical"] != fromSeed["Script reports critical"] {
			t.Errorf("taken's critical rule = %s\nwant SeedForOrg's %s", gotTaken["Script reports critical"], fromSeed["Script reports critical"])
		}
		if gotTaken["Script reports warning"] != operatorRow {
			t.Errorf("taken's warning rule = %s\nwant the operator's own %s", gotTaken["Script reports warning"], operatorRow)
		}
	}
	assertAfterUp(t)

	// Down removes exactly the seed rows of the two names — the
	// operator's row stays — and Up puts them back without a duplicate.
	sqlDB := stdlib.OpenDBFromPool(d.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS(filepath.Join("..", "db", "migrations")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 93); err != nil {
		t.Fatalf("roll back 00094: %v", err)
	}
	var seeds, operators, others int
	for _, c := range []struct {
		n     *int
		query string
	}{
		{&seeds, `SELECT count(*) FROM check_rules WHERE name = ANY($1) AND origin = 'seed'`},
		{&operators, `SELECT count(*) FROM check_rules WHERE name = ANY($1) AND origin = 'user'`},
		{&others, `SELECT count(*) FROM check_rules WHERE NOT name = ANY($1)`},
	} {
		if err := sqlDB.QueryRowContext(ctx, c.query, scriptRuleNames).Scan(c.n); err != nil {
			t.Fatal(err)
		}
	}
	// others: the five rules SeedForOrg wrote for "created" besides the
	// script pair — its SNMP pair is gone with 00097's Down, which
	// DownTo(93) ran first.
	if seeds != 0 || operators != 1 || others != 5 {
		t.Fatalf("after Down: %d seed rows, %d operator rows, %d other rows; want 0, 1 and 5", seeds, operators, others)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply 00094: %v", err)
	}
	assertAfterUp(t)
	if again := scriptRuleRows(ctx, t, d, created); len(again) != len(scriptRuleNames) {
		t.Fatalf("created after Down and Up = %v, want both rules once", again)
	}
}
