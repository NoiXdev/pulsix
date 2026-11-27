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

// snmpAssertionRuleNames are the two seed rules migration 00135
// backfills (backlog 3.134).
var snmpAssertionRuleNames = []string{"SNMP assertion failed", "SNMP assertion failed (critical)"}

// TestAssertionRulesMigrationMatchesSeedForOrg (backlog 3.87): migration
// 00134 writes the six assertion seed rules for an organization that
// existed before it, SeedForOrg writes them for one created afterwards, and
// the two agree on every column but the ids, organization and timestamps.
// An organization already holding a rule of one of the names keeps it and
// gets no second one; the Down removes only the seed rows.
func TestAssertionRulesMigrationMatchesSeedForOrg(t *testing.T) {
	checkAssertionSeedMigration(t, 134, assertionRuleNames, "TLS assertion failed")
}

// TestSNMPAssertionRulesMigrationMatchesSeedForOrg (backlog 3.134): the
// same for migration 00135 and the two snmp assertion rules, an operator's
// rule of one of the names left as it is.
func TestSNMPAssertionRulesMigrationMatchesSeedForOrg(t *testing.T) {
	checkAssertionSeedMigration(t, 135, snmpAssertionRuleNames, "SNMP assertion failed (critical)")
}

// checkAssertionSeedMigration checks the backfill migration version that
// seeds names: up to version-1, an organization plain without rules and
// one, taken, holding an operator's rule named operatorName; after the
// migration plain holds SeedForOrg's rows, taken its own rule and the rest,
// and the Down removes the seed rows alone.
func checkAssertionSeedMigration(t *testing.T, version int64, names []string, operatorName string) {
	t.Helper()
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, version-1); err != nil {
		t.Fatal(err)
	}
	plain, taken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'plain', 'plain'), ($2, 'taken', 'taken')`, []any{plain, taken}},
		{`INSERT INTO check_rules (id, org_id, name, origin, scope, check_key, metric, severity, mode, ast, source)
		  VALUES (gen_random_uuid(), $1, $2, 'user', 'org', 'cpu', 'cpu.used_pct', 'warn', 'expression',
		  '{"kind":"compare","op":">","args":[{"kind":"metric","name":"cpu.used_pct"},{"kind":"number","num":50}]}', 'cpu.used_pct > 50')`, []any{taken, operatorName}},
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
	fromSeed := namedRuleRows(ctx, t, d, created, names)
	if len(fromSeed) != len(names) {
		t.Fatalf("SeedForOrg wrote %v, want %v", fromSeed, names)
	}

	operatorRow := namedRuleRows(ctx, t, d, taken, names)[operatorName]
	assertAfterUp := func(t *testing.T) {
		t.Helper()
		fromMigration := namedRuleRows(ctx, t, d, plain, names)
		if len(fromMigration) != len(names) {
			t.Fatalf("migration %d wrote %v for plain, want %v", version, fromMigration, names)
		}
		for name, want := range fromSeed {
			if fromMigration[name] != want {
				t.Errorf("%s from migration %d = %s\nwant SeedForOrg's %s", name, version, fromMigration[name], want)
			}
		}
		gotTaken := namedRuleRows(ctx, t, d, taken, names)
		if len(gotTaken) != len(names) {
			t.Fatalf("taken holds %v, want every name once", gotTaken)
		}
		if gotTaken[operatorName] != operatorRow {
			t.Errorf("taken's %s = %s\nwant the operator's own %s", operatorName, gotTaken[operatorName], operatorRow)
		}
	}
	assertAfterUp(t)

	sqlDB := stdlib.OpenDBFromPool(d.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS(filepath.Join("..", "db", "migrations")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, version-1); err != nil {
		t.Fatalf("roll back %d: %v", version, err)
	}
	var seeds, operators int
	for _, c := range []struct {
		n     *int
		query string
	}{
		{&seeds, `SELECT count(*) FROM check_rules WHERE name = ANY($1) AND origin = 'seed'`},
		{&operators, `SELECT count(*) FROM check_rules WHERE name = ANY($1) AND origin = 'user'`},
	} {
		if err := sqlDB.QueryRowContext(ctx, c.query, names).Scan(c.n); err != nil {
			t.Fatal(err)
		}
	}
	if seeds != 0 || operators != 1 {
		t.Fatalf("after Down: %d seed rows, %d operator rows; want 0 and 1", seeds, operators)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply %d: %v", version, err)
	}
	assertAfterUp(t)
}
