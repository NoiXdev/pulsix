package db_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedRow is the part of a check_definitions row migration 00084 writes.
type seedRow struct {
	plugin, params, origin, binding string
	interval                        int
}

// orgScopeRows returns orgID's organization-wide check definitions keyed by
// key.
func orgScopeRows(ctx context.Context, t *testing.T, d *db.DB, orgID uuid.UUID) map[string]seedRow {
	t.Helper()
	rows, err := d.Pool().Query(ctx, `
SELECT key, plugin, params::text, origin, binding_kind, interval_seconds
FROM check_definitions WHERE org_id = $1 AND binding_kind = 'org'`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]seedRow{}
	for rows.Next() {
		var key string
		var r seedRow
		if err := rows.Scan(&key, &r.plugin, &r.params, &r.origin, &r.binding, &r.interval); err != nil {
			t.Fatal(err)
		}
		got[key] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestNetworkUptimeSeedMigration (backlog 3.7): migration 00084 gives every
// existing organization the network and uptime seed rows SeedForOrg gives a
// new one, and leaves an organization alone whose operator already used
// either key at the organization-wide scope — the migration must not fail
// there, and must not replace the operator's row. Its Down removes only the
// seed rows it added, so a Down/Up round trip lands in the same state.
func TestNetworkUptimeSeedMigration(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 83); err != nil {
		t.Fatal(err)
	}

	plain, taken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zone, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'plain', 'plain'), ($2, 'taken', 'taken')`, []any{plain, taken}},
		{`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, []any{zone, plain}},
		{`INSERT INTO hosts (id, org_id, zone_id, name) VALUES ($1, $2, $3, 'h')`, []any{host, plain, zone}},
		// The operator of "taken" already has an org-wide check under the
		// key "network" — a different plugin even — and it must survive.
		{`INSERT INTO check_definitions (id, org_id, key, plugin, params, interval_seconds, origin, binding_kind)
		  VALUES (gen_random_uuid(), $1, 'network', 'service', '{"units":["networkd"]}', 30, 'custom', 'org')`, []any{taken}},
		// A host-bound "uptime" in "plain" is a different scope, so it does
		// not stop the organization-wide seed.
		{`INSERT INTO check_definitions (id, org_id, key, plugin, params, interval_seconds, origin, binding_kind, host_id)
		  VALUES (gen_random_uuid(), $1, 'uptime', 'cpu', '{}', 30, 'custom', 'host', $2)`, []any{plain, host}},
	} {
		if _, err := d.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%.40s: %v", stmt.sql, err)
		}
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	wantSeeds := map[string]seedRow{
		"network": {plugin: "network", params: "{}", origin: "seed", binding: "org", interval: 60},
		"uptime":  {plugin: "uptime", params: "{}", origin: "seed", binding: "org", interval: 300},
	}
	assertAfterUp := func(t *testing.T) {
		t.Helper()
		gotPlain := orgScopeRows(ctx, t, d, plain)
		for key, want := range wantSeeds {
			if gotPlain[key] != want {
				t.Errorf("plain %q = %+v, want %+v", key, gotPlain[key], want)
			}
		}
		gotTaken := orgScopeRows(ctx, t, d, taken)
		operator := seedRow{plugin: "service", params: `{"units": ["networkd"]}`, origin: "custom", binding: "org", interval: 30}
		if gotTaken["network"] != operator {
			t.Errorf("taken network = %+v, want the operator's own row %+v", gotTaken["network"], operator)
		}
		if gotTaken["uptime"] != wantSeeds["uptime"] {
			t.Errorf("taken uptime = %+v, want %+v", gotTaken["uptime"], wantSeeds["uptime"])
		}
		var hostBound int
		if err := d.Pool().QueryRow(ctx,
			`SELECT count(*) FROM check_definitions WHERE org_id = $1 AND key = 'uptime' AND binding_kind = 'host' AND origin = 'custom'`,
			plain).Scan(&hostBound); err != nil {
			t.Fatal(err)
		}
		if hostBound != 1 {
			t.Errorf("host-bound uptime rows = %d, want the operator's 1", hostBound)
		}
	}
	assertAfterUp(t)

	// Down removes exactly the seed rows, Up puts them back.
	sqlDB := stdlib.OpenDBFromPool(d.Pool())
	defer sqlDB.Close() //nolint:errcheck
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 83); err != nil {
		t.Fatalf("roll back 00084: %v", err)
	}
	var seeds, operators int
	if err := countRows(ctx, sqlDB, &seeds, `SELECT count(*) FROM check_definitions WHERE key IN ('network', 'uptime') AND origin = 'seed'`); err != nil {
		t.Fatal(err)
	}
	if err := countRows(ctx, sqlDB, &operators, `SELECT count(*) FROM check_definitions WHERE key IN ('network', 'uptime') AND origin = 'custom'`); err != nil {
		t.Fatal(err)
	}
	if seeds != 0 || operators != 2 {
		t.Fatalf("after Down: %d seed rows, %d operator rows; want 0 and 2", seeds, operators)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply 00084: %v", err)
	}
	assertAfterUp(t)
}

func countRows(ctx context.Context, q *sql.DB, n *int, query string) error {
	return q.QueryRowContext(ctx, query).Scan(n)
}
