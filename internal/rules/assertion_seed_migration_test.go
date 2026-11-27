package rules_test

import (
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

// assertionRuleNames are the six seed rules migration 00134 backfills.
var assertionRuleNames = []string{
	"TCP assertion failed", "TCP assertion failed (critical)",
	"DNS assertion failed", "DNS assertion failed (critical)",
	"TLS assertion failed", "TLS assertion failed (critical)",
}

// TestAssertionRulesMigrationMatchesSeedForOrg (backlog 3.87): migration
// 00134 writes the six assertion seed rules for an organization that
// existed before it, SeedForOrg writes them for one created afterwards, and
// the two agree on every column but the ids, organization and timestamps.
// An organization already holding a rule of one of the names keeps it and
// gets no second one; the Down removes only the seed rows.
func TestAssertionRulesMigrationMatchesSeedForOrg(t *testing.T) {
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 133); err != nil {
		t.Fatal(err)
	}
	plain, taken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'plain', 'plain'), ($2, 'taken', 'taken')`, []any{plain, taken}},
		{`INSERT INTO check_rules (id, org_id, name, origin, scope, check_key, metric, severity, mode, ast, source)
		  VALUES (gen_random_uuid(), $1, 'TLS assertion failed', 'user', 'org', 'cpu', 'cpu.used_pct', 'warn', 'expression',
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
	fromSeed := namedRuleRows(ctx, t, d, created, assertionRuleNames)
	if len(fromSeed) != len(assertionRuleNames) {
		t.Fatalf("SeedForOrg wrote %v, want %v", fromSeed, assertionRuleNames)
	}

	operatorRow := namedRuleRows(ctx, t, d, taken, assertionRuleNames)["TLS assertion failed"]
	assertAfterUp := func(t *testing.T) {
		t.Helper()
		fromMigration := namedRuleRows(ctx, t, d, plain, assertionRuleNames)
		if len(fromMigration) != len(assertionRuleNames) {
			t.Fatalf("migration 00134 wrote %v for plain, want %v", fromMigration, assertionRuleNames)
		}
		for name, want := range fromSeed {
			if fromMigration[name] != want {
				t.Errorf("%s from migration 00134 = %s\nwant SeedForOrg's %s", name, fromMigration[name], want)
			}
		}
		gotTaken := namedRuleRows(ctx, t, d, taken, assertionRuleNames)
		if len(gotTaken) != len(assertionRuleNames) {
			t.Fatalf("taken holds %v, want every name once", gotTaken)
		}
		if gotTaken["TLS assertion failed"] != operatorRow {
			t.Errorf("taken's tls rule = %s\nwant the operator's own %s", gotTaken["TLS assertion failed"], operatorRow)
		}
	}
	assertAfterUp(t)

	sqlDB := stdlib.OpenDBFromPool(d.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS(filepath.Join("..", "db", "migrations")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 133); err != nil {
		t.Fatalf("roll back 00134: %v", err)
	}
	var seeds, operators int
	for _, c := range []struct {
		n     *int
		query string
	}{
		{&seeds, `SELECT count(*) FROM check_rules WHERE name = ANY($1) AND origin = 'seed'`},
		{&operators, `SELECT count(*) FROM check_rules WHERE name = ANY($1) AND origin = 'user'`},
	} {
		if err := sqlDB.QueryRowContext(ctx, c.query, assertionRuleNames).Scan(c.n); err != nil {
			t.Fatal(err)
		}
	}
	if seeds != 0 || operators != 1 {
		t.Fatalf("after Down: %d seed rows, %d operator rows; want 0 and 1", seeds, operators)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply 00134: %v", err)
	}
	assertAfterUp(t)
}
