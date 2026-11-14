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

// snmpRuleNames are the two seed rules migration 00097 backfills.
var snmpRuleNames = []string{"SNMP device unreachable", "Interface down"}

// TestSNMPRulesMigrationMatchesSeedForOrg (backlog 3.13d): migration 00097
// writes the two SNMP seed rules for an organization that existed before
// it, SeedForOrg writes them for one created afterwards, and the two must
// agree on every column but the ids, organization and timestamps — the
// migration encodes SeedRules() by hand, so this is what notices the two
// drifting apart. An organization already holding a rule of either name
// keeps it and gets no second one; the Down removes only the seed rows.
func TestSNMPRulesMigrationMatchesSeedForOrg(t *testing.T) {
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 96); err != nil {
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
		  VALUES (gen_random_uuid(), $1, 'Interface down', 'user', 'org', 'cpu', 'cpu.used_pct', 'crit', 'expression',
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
	fromSeed := namedRuleRows(ctx, t, d, created, snmpRuleNames)
	if len(fromSeed) != len(snmpRuleNames) {
		t.Fatalf("SeedForOrg wrote %v, want %v", fromSeed, snmpRuleNames)
	}

	operatorRow := namedRuleRows(ctx, t, d, taken, snmpRuleNames)["Interface down"]
	assertAfterUp := func(t *testing.T) {
		t.Helper()
		fromMigration := namedRuleRows(ctx, t, d, plain, snmpRuleNames)
		if len(fromMigration) != len(snmpRuleNames) {
			t.Fatalf("migration 00097 wrote %v for plain, want %v", fromMigration, snmpRuleNames)
		}
		for name, want := range fromSeed {
			if fromMigration[name] != want {
				t.Errorf("%s from migration 00097 = %s\nwant SeedForOrg's %s", name, fromMigration[name], want)
			}
		}
		gotTaken := namedRuleRows(ctx, t, d, taken, snmpRuleNames)
		if gotTaken["SNMP device unreachable"] != fromSeed["SNMP device unreachable"] {
			t.Errorf("taken's unreachable rule = %s\nwant SeedForOrg's %s", gotTaken["SNMP device unreachable"], fromSeed["SNMP device unreachable"])
		}
		if gotTaken["Interface down"] != operatorRow {
			t.Errorf("taken's interface rule = %s\nwant the operator's own %s", gotTaken["Interface down"], operatorRow)
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
	if _, err := provider.DownTo(ctx, 96); err != nil {
		t.Fatalf("roll back 00097: %v", err)
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
		if err := sqlDB.QueryRowContext(ctx, c.query, snmpRuleNames).Scan(c.n); err != nil {
			t.Fatal(err)
		}
	}
	// others: the seven rules SeedForOrg wrote for "created" besides the
	// SNMP pair (plain and taken came after 00045 and 00094 ran).
	if seeds != 0 || operators != 1 || others != 7 {
		t.Fatalf("after Down: %d seed rows, %d operator rows, %d other rows; want 0, 1 and 7", seeds, operators, others)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply 00097: %v", err)
	}
	assertAfterUp(t)
	if again := namedRuleRows(ctx, t, d, created, snmpRuleNames); len(again) != len(snmpRuleNames) {
		t.Fatalf("created after Down and Up = %v, want both rules once", again)
	}
}
