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

// webRuleNames are the seven seed rules migration 00104 backfills.
var webRuleNames = []string{
	"Web assertion failed", "Web assertion failed (critical)", "Ping check down",
	"TCP check down", "HTTP check down", "TLS check down", "DNS check down",
}

// TestWebRulesMigrationMatchesSeedForOrg (backlog 3.13b): migration 00104
// writes the seven synthetic-check seed rules for an organization that
// existed before it, SeedForOrg writes them for one created afterwards, and
// the two must agree on every column but the ids, organization and
// timestamps. An organization already holding a rule of one of the names
// keeps it and gets no second one; the Down removes only the seed rows.
func TestWebRulesMigrationMatchesSeedForOrg(t *testing.T) {
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 103); err != nil {
		t.Fatal(err)
	}
	plain, taken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'plain', 'plain'), ($2, 'taken', 'taken')`, []any{plain, taken}},
		{`INSERT INTO check_rules (id, org_id, name, origin, scope, check_key, metric, severity, mode, ast, source)
		  VALUES (gen_random_uuid(), $1, 'Ping check down', 'user', 'org', 'cpu', 'cpu.used_pct', 'warn', 'expression',
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
	fromSeed := namedRuleRows(ctx, t, d, created, webRuleNames)
	if len(fromSeed) != len(webRuleNames) {
		t.Fatalf("SeedForOrg wrote %v, want %v", fromSeed, webRuleNames)
	}

	operatorRow := namedRuleRows(ctx, t, d, taken, webRuleNames)["Ping check down"]
	assertAfterUp := func(t *testing.T) {
		t.Helper()
		fromMigration := namedRuleRows(ctx, t, d, plain, webRuleNames)
		if len(fromMigration) != len(webRuleNames) {
			t.Fatalf("migration 00104 wrote %v for plain, want %v", fromMigration, webRuleNames)
		}
		for name, want := range fromSeed {
			if fromMigration[name] != want {
				t.Errorf("%s from migration 00104 = %s\nwant SeedForOrg's %s", name, fromMigration[name], want)
			}
		}
		gotTaken := namedRuleRows(ctx, t, d, taken, webRuleNames)
		if len(gotTaken) != len(webRuleNames) {
			t.Fatalf("taken holds %v, want every name once", gotTaken)
		}
		for name, want := range fromSeed {
			if name == "Ping check down" {
				continue
			}
			if gotTaken[name] != want {
				t.Errorf("taken's %s = %s\nwant SeedForOrg's %s", name, gotTaken[name], want)
			}
		}
		if gotTaken["Ping check down"] != operatorRow {
			t.Errorf("taken's ping rule = %s\nwant the operator's own %s", gotTaken["Ping check down"], operatorRow)
		}
	}
	assertAfterUp(t)

	sqlDB := stdlib.OpenDBFromPool(d.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS(filepath.Join("..", "db", "migrations")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 103); err != nil {
		t.Fatalf("roll back 00104: %v", err)
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
		if err := sqlDB.QueryRowContext(ctx, c.query, webRuleNames).Scan(c.n); err != nil {
			t.Fatal(err)
		}
	}
	// others: the nine older seed rules SeedForOrg wrote for "created".
	if seeds != 0 || operators != 1 || others != 9 {
		t.Fatalf("after Down: %d seed rows, %d operator rows, %d other rows; want 0, 1 and 9", seeds, operators, others)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply 00104: %v", err)
	}
	assertAfterUp(t)
}
