package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/testdb"
)

// exemptedFromRLS lists the org-owned tables that deliberately carry no
// row-level security.
//
// workers: GetBySerial/Touch resolve a worker by its certificate serial
// outside the pulsix.org_id scope (the gRPC interceptor has no org
// context; the serial is the secret and is globally unique), and every
// other statement filters by org_id in its WHERE clause. See
// internal/db/migrations/00017_workers.sql.
//
// job_events: the worker event path (gRPC, org-unscoped by design)
// appends without an org scope, so a policy pair would force every append
// through an explicit scope for no gain. The table is read only through
// jobs.Service.Events, which resolves the job's organization first and
// then filters by org_id in the WHERE clause. See
// internal/db/migrations/00018_jobs.sql.
var exemptedFromRLS = map[string]bool{"workers": true, "job_events": true}

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
		if exemptedFromRLS[name] {
			continue
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

// TestHypertableChunksDoNotInheritRowLevelSecurity is a characterization
// test: it pins what the database actually does, including the part it
// does NOT do, so the gap above is asserted rather than invisible.
//
// TestEveryOrgOwnedTableForcesRowLevelSecurity filters nspname='public'
// AND relkind='r', which is right for every ordinary table and silently
// excludes a hypertable's chunks. check_samples (00044) is the first
// RLS-PROTECTED hypertable in this schema, so nothing had ever needed to
// notice. Its chunks live in _timescaledb_internal, carry
// relrowsecurity=false, and get the parent's ACL copied onto them by
// TimescaleDB at creation: a query THROUGH the parent is scoped, a query
// naming the chunk is not.
//
// No mechanism was adopted, because every candidate was probed against
// timescale/timescaledb:2.30.0-pg18 and none holds:
//
//   - REVOKE USAGE ON SCHEMA _timescaledb_internal FROM PUBLIC does stop
//     a chunk being nameable, and reads, writes and chunk creation
//     through the parent survive it — but pulsix_app then cannot run ANY
//     statement at all, not even SELECT count(*) FROM hosts: TimescaleDB
//     needs that schema for its own per-session work. (Granting USAGE
//     straight back to pulsix_app restores everything, which is how the
//     cause was isolated.)
//   - REVOKE on the chunk tables themselves works, and only until the
//     next chunk: a chunk created afterwards comes up carrying
//     pulsix_app=arwd/pulsix again, copied from the hypertable.
//   - ALTER TABLE <chunk> ENABLE ROW LEVEL SECURITY works exactly as
//     wanted — with no policy of its own the chunk denies everything by
//     name, while the parent keeps reading, inserting, updating and
//     deleting normally — but nothing applies it to a chunk that does not
//     exist yet. A ddl_command_end event trigger does not: TimescaleDB
//     creates chunks without firing one (verified: a trigger on CREATE
//     TABLE never ran for a chunk an insert produced).
//
// The exposure this leaves is bounded by something already true of this
// schema: the org_isolation_bypass policy means any session that can
// execute arbitrary SQL as pulsix_app can read every organization by
// clearing pulsix.org_id, through the parent, without going near a chunk.
// Row-level security here is a guard against a missing scope in the
// application's own statements, not a barrier against arbitrary SQL — and
// no application code names a chunk.
//
// If this test starts FAILING, that is the good news: TimescaleDB or
// PostgreSQL changed, a mechanism may now exist, and migration 00044's
// note should be revisited.
func TestHypertableChunksDoNotInheritRowLevelSecurity(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()

	type org struct{ id, zone, host uuid.UUID }
	var orgs []org
	for _, slug := range []string{"rls-chunk-a", "rls-chunk-b"} {
		o := org{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		exec(t, admin, `INSERT INTO organizations (id, slug, name) VALUES ($1,$2,$2)`, o.id, slug)
		exec(t, admin, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1,$2,$3,$3)`, o.zone, o.id, slug)
		exec(t, admin, `INSERT INTO hosts (id, org_id, zone_id, name, facts) VALUES ($1,$2,$3,$4,'{}')`, o.host, o.id, o.zone, slug)
		exec(t, admin, `INSERT INTO check_samples (at, org_id, host_id, check_key, item, metric, value)
			VALUES (now(), $1, $2, 'cpu', '', 'cpu.used_pct', 1)`, o.id, o.host)
		orgs = append(orgs, o)
	}

	// Every chunk of check_samples, and whether it carries row-level
	// security of its own. None does, and nothing in the schema can make
	// the next one carry it either.
	rows, err := admin.Query(ctx, `SELECT format('%I.%I', ch.chunk_schema, ch.chunk_name), c.relrowsecurity
		FROM timescaledb_information.chunks ch
		JOIN pg_class c ON c.relname = ch.chunk_name
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = ch.chunk_schema
		WHERE ch.hypertable_name = 'check_samples'`)
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for rows.Next() {
		var name string
		var rls bool
		if err := rows.Scan(&name, &rls); err != nil {
			t.Fatal(err)
		}
		if rls {
			t.Errorf("chunk %s carries row-level security; TimescaleDB changed, revisit migration 00044's note", name)
		}
		chunks = append(chunks, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(chunks) == 0 {
		t.Fatal("check_samples produced no chunk; the fixture is wrong, not the database")
	}

	app, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(ctx) }()
	exec(t, app, `SET ROLE pulsix_app`)
	exec(t, app, `SELECT set_config('pulsix.org_id', $1, false)`, orgs[0].id.String())

	// The guarantee that IS real: through the parent, one organization.
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM check_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("rows visible through the hypertable parent = %d, want 1 (this organization's own)", n)
	}

	// The gap, stated: naming the chunk sees both organizations.
	if err := app.QueryRow(ctx, `SELECT count(*) FROM `+chunks[0]).Scan(&n); err != nil {
		t.Fatalf("naming chunk %s: %v", chunks[0], err)
	}
	if n != 2 {
		t.Errorf("rows visible by naming chunk %s = %d, want 2 — if this is now 1 or an error, "+
			"the chunk gained protection and migration 00044's note should be revisited", chunks[0], n)
	}
}

// exec runs one statement or fails the test with the statement that broke.
func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
