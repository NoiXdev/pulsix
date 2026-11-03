package backup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

var testKey = [32]byte{1, 2, 3}

// sourceDB opens the shared, migrated test database as its superuser, which
// owns the schema there. Every table is truncated when the test ends.
func sourceDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// targetDB opens a fresh, empty database as its non-superuser owner.
func targetDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func schemaVersion(t *testing.T, d *db.DB) int64 {
	t.Helper()
	v, err := d.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func dumpBytes(t *testing.T, pool *pgxpool.Pool, opts backup.DumpOptions) ([]byte, backup.Header) {
	t.Helper()
	if opts.Kind == "" {
		opts.Kind = "manual"
	}
	if opts.PulsixVersion == "" {
		opts.PulsixVersion = "1.2.3"
	}
	var buf bytes.Buffer
	h, err := backup.Dump(context.Background(), pool, testKey, &buf, opts)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	return buf.Bytes(), h
}

func opener(data []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
}

func restoreBytes(target *db.DB, key [32]byte, data []byte, opts backup.RestoreOptions) (backup.Manifest, error) {
	return backup.Restore(context.Background(), target, key, opener(data), opts)
}

// publicTables lists the tables the migrations created, goose's own aside.
func publicTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func rowCount(t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM "+pgx.Identifier{"public", table}.Sanitize()).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// tableDigest is an order-independent fingerprint of a table's rows.
func tableDigest(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var digest *string
	if err := pool.QueryRow(context.Background(),
		"SELECT md5(string_agg(t::text, '|' ORDER BY t::text)) FROM "+pgx.Identifier{"public", table}.Sanitize()+" t",
	).Scan(&digest); err != nil {
		t.Fatalf("digest %s: %v", table, err)
	}
	if digest == nil {
		return "<empty>"
	}
	return *digest
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// endedByRestore are the tables a restore empties on purpose: it signs
// everybody out (TestRestoreEndsAllSessions).
var endedByRestore = map[string]bool{"sessions": true}

// restoredDigest is the digest table should have after restoring a backup
// of pool: the source's, or empty for a table the restore ends.
func restoredDigest(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	if endedByRestore[table] {
		return "<empty>"
	}
	return tableDigest(t, pool, table)
}

func TestRoundTripRestoresEveryRow(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	data, h := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})

	target := targetDB(t)
	m, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if m.Header.SchemaVersion != h.SchemaVersion || !m.Header.CreatedAt.Equal(h.CreatedAt) {
		t.Errorf("restore returned header %+v, dump wrote %+v", m.Header, h)
	}

	tables := publicTables(t, src.Pool())
	if got := publicTables(t, target.Pool()); !slices.Equal(got, tables) {
		t.Fatalf("target tables %v, want %v", got, tables)
	}
	for _, name := range tables {
		if got, want := tableDigest(t, target.Pool(), name), restoredDigest(t, src.Pool(), name); got != want {
			t.Errorf("%s: restored rows differ from the source (digest %s, want %s)", name, got, want)
		}
	}
	if got, want := sequenceValues(t, target.Pool()), sequenceValues(t, src.Pool()); !slices.Equal(got, want) {
		t.Errorf("sequences after restore %v, want %v", got, want)
	}
	if v := schemaVersion(t, target); v != schemaVersion(t, src) {
		t.Errorf("target schema version %d, want %d", v, schemaVersion(t, src))
	}
}

// sequenceValues lists every public sequence but goose's with its state.
func sequenceValues(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_class c
		WHERE c.relkind = 'S' AND c.relnamespace = 'public'::regnamespace AND c.relname <> 'goose_db_version_id_seq'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range names {
		var last int64
		var called bool
		if err := pool.QueryRow(ctx, "SELECT last_value, is_called FROM "+pgx.Identifier{"public", name}.Sanitize()).Scan(&last, &called); err != nil {
			t.Fatal(err)
		}
		out = append(out, name+"="+strconv.FormatInt(last, 10)+"/"+strconv.FormatBool(called))
	}
	return out
}

func TestDumpedTablesEqualMigratedTables(t *testing.T) {
	src := sourceDB(t)
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	m, err := backup.Verify(bytes.NewReader(data), testKey)
	if err != nil {
		t.Fatal(err)
	}
	var dumped []string
	for _, tc := range m.Tables {
		dumped = append(dumped, tc.Name)
	}
	slices.Sort(dumped)
	if want := publicTables(t, src.Pool()); !slices.Equal(dumped, want) {
		t.Errorf("dumped tables %v\nwant the migrated tables %v", dumped, want)
	}
	for _, name := range []string{"job_events", "check_samples", "check_state_changes", "instance_ca", "users"} {
		if !slices.Contains(dumped, name) {
			t.Errorf("%s is missing from the dump", name)
		}
	}
}

func TestDumpWithoutMetricsLeavesCheckSamplesEmpty(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	data, h := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: false})
	if h.IncludesMetrics {
		t.Error("header says metrics are included")
	}

	target := targetDB(t)
	m, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, tc := range m.Tables {
		if tc.Name == "check_samples" {
			t.Error("the manifest lists check_samples although metrics were left out")
		}
	}
	for _, name := range publicTables(t, src.Pool()) {
		got, want := rowCount(t, target.Pool(), name), rowCount(t, src.Pool(), name)
		if name == "check_samples" || endedByRestore[name] {
			want = 0
		}
		if got != want {
			t.Errorf("%s: %d rows restored, want %d", name, got, want)
		}
	}
}

func TestDumpAsAppRoleSeesEveryOrganization(t *testing.T) {
	src := sourceDB(t)
	exec(t, src.Pool(), `
		INSERT INTO organizations (id, slug, name) VALUES
			('00000000-0000-4000-8000-0000000000a1', 'org-a', 'A'),
			('00000000-0000-4000-8000-0000000000b1', 'org-b', 'B');
		INSERT INTO zones (id, org_id, slug, name) VALUES
			('00000000-0000-4000-8000-0000000000a2', '00000000-0000-4000-8000-0000000000a1', 'default', 'Default'),
			('00000000-0000-4000-8000-0000000000b2', '00000000-0000-4000-8000-0000000000b1', 'default', 'Default');
		INSERT INTO hosts (id, org_id, zone_id, name) VALUES
			('00000000-0000-4000-8000-0000000000a3', '00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-0000000000a2', 'a-host'),
			('00000000-0000-4000-8000-0000000000b3', '00000000-0000-4000-8000-0000000000b1', '00000000-0000-4000-8000-0000000000b2', 'b-host');`)

	app := testdb.Open(t) // pulsix_app, no organization scope
	data, _ := dumpBytes(t, app.Pool(), backup.DumpOptions{IncludeMetrics: true})

	target := targetDB(t)
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rows, err := target.Pool().Query(context.Background(), `SELECT name FROM hosts ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"a-host", "b-host"}) {
		t.Errorf("restored hosts %v, want both organizations' hosts", names)
	}
}

// forcedTables maps every public table to whether it forces row-level
// security.
func forcedTables(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT relname, relforcerowsecurity FROM pg_class
		WHERE relnamespace = 'public'::regnamespace AND relkind IN ('r', 'p') AND relname <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	var name string
	var forced bool
	if _, err := pgx.ForEachRow(rows, []any{&name, &forced}, func() error {
		out[name] = forced
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// foreignKeys lists every public foreign key as "table constraint definition".
func foreignKeys(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT c.conrelid::regclass::text || ' ' || c.conname || ' ' || pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRestorePreservesForcedRLS covers Review Focus 3: the load lifts FORCE
// ROW LEVEL SECURITY and drops the foreign keys inside its transaction, and
// both must be exactly as the migrations left them afterwards.
func TestRestorePreservesForcedRLS(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	target := targetDB(t)
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	want, got := forcedTables(t, src.Pool()), forcedTables(t, target.Pool())
	forced := 0
	for name, f := range want {
		if got[name] != f {
			t.Errorf("%s: relforcerowsecurity %v after restore, want %v", name, got[name], f)
		}
		if f {
			forced++
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d tables after restore, want %d", len(got), len(want))
	}
	// The design counted 57 forced tables; migrations since have added more.
	// The floor keeps the comparison above from passing on an empty catalogue.
	if forced < 57 {
		t.Errorf("only %d tables force row-level security; the catalogue query is wrong", forced)
	}

	if got, want := foreignKeys(t, target.Pool()), foreignKeys(t, src.Pool()); !slices.Equal(got, want) {
		t.Errorf("foreign keys after restore:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	} else if len(want) == 0 {
		t.Error("no foreign keys found; the catalogue query is wrong")
	}
}

// TestRestoreSkipsGeneratedColumns covers Review Focus 2: a generated column
// is neither dumped nor loaded (the target computes it), and an identity
// column keeps its values and its sequence.
func TestRestoreSkipsGeneratedColumns(t *testing.T) {
	const probe = `CREATE TABLE zz_backup_probe (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		a int,
		b int GENERATED ALWAYS AS (a * 2) STORED)`
	src := sourceDB(t)
	exec(t, src.Pool(), probe)
	t.Cleanup(func() {
		if _, err := src.Pool().Exec(context.Background(), `DROP TABLE IF EXISTS zz_backup_probe`); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
	})
	exec(t, src.Pool(), `INSERT INTO zz_backup_probe (a) VALUES (1), (2), (3); DELETE FROM zz_backup_probe WHERE a = 2`)

	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	m, err := backup.Verify(bytes.NewReader(data), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(m.Sequences, func(s backup.SequenceValue) bool {
		return s.Name == "zz_backup_probe_id_seq" && s.LastValue == 3 && s.IsCalled
	}) {
		t.Errorf("manifest sequences %+v lack zz_backup_probe_id_seq at 3", m.Sequences)
	}

	target := targetDB(t)
	opts := backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)}
	backup.SetAfterMigrate(&opts, func(pool *pgxpool.Pool) error {
		_, err := pool.Exec(context.Background(), probe)
		return err
	})
	if _, err := restoreBytes(target, testKey, data, opts); err != nil {
		t.Fatalf("restore: %v", err)
	}

	rows, err := target.Pool().Query(context.Background(), `SELECT id::text || ':' || a || ':' || b FROM zz_backup_probe ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1:1:2", "3:3:6"}; !slices.Equal(got, want) {
		t.Errorf("restored probe rows %v, want %v", got, want)
	}
	// The identity sequence came back too: the next row does not collide.
	var next int64
	if err := target.Pool().QueryRow(context.Background(), `INSERT INTO zz_backup_probe (a) VALUES (4) RETURNING id`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Errorf("next identity value %d, want 4", next)
	}
}

// seededTarget is a fully migrated fresh database with one user in it, so it
// counts as not empty.
func seededTarget(t *testing.T) *db.DB {
	t.Helper()
	target := targetDB(t)
	if err := target.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	exec(t, target.Pool(), `INSERT INTO users (id, email, display_name) VALUES ('00000000-0000-4000-8000-0000000000ee', 'old@example.com', 'Old')`)
	return target
}

// assertUntouched fails unless target still has seededTarget's user and the
// schema version it had before.
func assertUntouched(t *testing.T, target *db.DB, version int64) {
	t.Helper()
	var email string
	if err := target.Pool().QueryRow(context.Background(), `SELECT email FROM users`).Scan(&email); err != nil {
		t.Fatalf("the target lost its user: %v", err)
	}
	if email != "old@example.com" {
		t.Errorf("target user %q, want old@example.com", email)
	}
	if v := schemaVersion(t, target); v != version {
		t.Errorf("target schema version %d, want %d", v, version)
	}
}

func TestRestoreRefusesNonEmptyWithoutReplace(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	newest := schemaVersion(t, src)

	target := seededTarget(t)
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: newest}); !errors.Is(err, backup.ErrNotEmpty) {
		t.Fatalf("restore into a non-empty database: %v, want ErrNotEmpty", err)
	}
	assertUntouched(t, target, newest)

	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest}); err != nil {
		t.Fatalf("restore with Replace: %v", err)
	}
	rows, err := target.Pool().Query(context.Background(), `SELECT email::text FROM users ORDER BY email`)
	if err != nil {
		t.Fatal(err)
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(emails, []string{"fixture@example.com"}) {
		t.Errorf("users after replace %v, want only the backup's user", emails)
	}

	// Wiping dropped the old hypertables; TimescaleDB's retention and
	// compression jobs must have gone with them, not linger next to the
	// migrations' new ones.
	if got, want := policyJobs(t, target.Pool()), policyJobs(t, src.Pool()); !slices.Equal(got, want) {
		t.Errorf("TimescaleDB policy jobs after replace %v, want %v", got, want)
	}
}

// policyJobs lists TimescaleDB's per-hypertable jobs as "procedure on table",
// counting jobs whose hypertable no longer exists as "procedure on <gone>".
func policyJobs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT j.proc_name || ' on ' || COALESCE(h.table_name::text, '<gone>')
		FROM _timescaledb_config.bgw_job j
		LEFT JOIN _timescaledb_catalog.hypertable h ON h.id = j.hypertable_id
		WHERE j.hypertable_id IS NOT NULL
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 0 {
		t.Fatal("no TimescaleDB policy jobs at all; the query is wrong")
	}
	return jobs
}

func TestRestoreRefusesNewerBackup(t *testing.T) {
	src := sourceDB(t)
	data, h := dumpBytes(t, src.Pool(), backup.DumpOptions{PulsixVersion: "9.8.7"})
	target := seededTarget(t)
	newest := schemaVersion(t, target)

	_, err := restoreBytes(target, testKey, data, backup.RestoreOptions{Replace: true, BinarySchemaVersion: h.SchemaVersion - 1})
	if !errors.Is(err, backup.ErrBackupTooNew) {
		t.Fatalf("restore of a newer backup: %v, want ErrBackupTooNew", err)
	}
	if !strings.Contains(err.Error(), "9.8.7") {
		t.Errorf("error %q does not name the backup's Pulsix version", err)
	}
	assertUntouched(t, target, newest)
}

// TestRestoreFromOlderSchemaVersion restores a backup taken at schema 60 into
// a binary that knows more migrations: the load happens at 60 and the rest
// of the migrations run afterwards.
func TestRestoreFromOlderSchemaVersion(t *testing.T) {
	ctx := context.Background()
	newest := schemaVersion(t, sourceDB(t))

	old := targetDB(t)
	if err := old.MigrateTo(ctx, 60); err != nil {
		t.Fatal(err)
	}
	exec(t, old.Pool(), `INSERT INTO organizations (id, slug, name) VALUES ('00000000-0000-4000-8000-0000000000c1', 'old-org', 'Old')`)
	data, h := dumpBytes(t, old.Pool(), backup.DumpOptions{IncludeMetrics: true})
	if h.SchemaVersion != 60 {
		t.Fatalf("dump of a database at 60 says schema %d", h.SchemaVersion)
	}

	target := targetDB(t)
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: newest}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if v := schemaVersion(t, target); v != newest {
		t.Errorf("schema version after restore %d, want %d", v, newest)
	}
	var slug string
	if err := target.Pool().QueryRow(ctx, `SELECT slug FROM organizations`).Scan(&slug); err != nil || slug != "old-org" {
		t.Errorf("restored organization %q, %v; want old-org", slug, err)
	}
}

// bulkSource fills the source with enough rows that cutting the file's last
// KiB lands inside the encrypted payload rather than shortening it to a
// stub.
func bulkSource(t *testing.T) *db.DB {
	t.Helper()
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	exec(t, src.Pool(), `
		INSERT INTO audit_events (id, actor_kind, action, object_type, object_id, summary, at)
		SELECT gen_random_uuid(), 'system', 'bulk', 'row', i::text, md5(i::text) || md5((i * 7)::text), now()
		FROM generate_series(1, 500) i`)
	return src
}

// TestCorruptFileLeavesTargetUntouched covers Review Focus 1: a cut-off file
// given to a replacing restore must fail before anything is dropped.
func TestCorruptFileLeavesTargetUntouched(t *testing.T) {
	src := bulkSource(t)
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	if len(data) < 8<<10 {
		t.Fatalf("backup is only %d bytes; cutting 1 KiB proves little", len(data))
	}
	target := seededTarget(t)
	newest := schemaVersion(t, target)

	_, err := restoreBytes(target, testKey, data[:len(data)-1024], backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest})
	if !errors.Is(err, backup.ErrCorrupt) {
		t.Fatalf("restore of a truncated file: %v, want ErrCorrupt", err)
	}
	assertUntouched(t, target, newest)
}

func TestWrongKeyLeavesTargetUntouched(t *testing.T) {
	src := bulkSource(t)
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	target := seededTarget(t)
	newest := schemaVersion(t, target)

	_, err := restoreBytes(target, [32]byte{9}, data, backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest})
	if !errors.Is(err, backup.ErrWrongKey) {
		t.Fatalf("restore with another master key: %v, want ErrWrongKey", err)
	}
	assertUntouched(t, target, newest)
}

// TestRestoreDecidesOnTheAuthenticatedHeader: the plaintext header is not
// authenticated, so a file whose header was edited to claim a much newer
// schema is corrupt, not "too new", and nothing is touched.
func TestRestoreDecidesOnTheAuthenticatedHeader(t *testing.T) {
	src := sourceDB(t)
	data, h := dumpBytes(t, src.Pool(), backup.DumpOptions{})
	target := seededTarget(t)
	newest := schemaVersion(t, target)

	tampered := retitle(t, data, func(x *backup.Header) { x.SchemaVersion = h.SchemaVersion + 1000 })
	_, err := restoreBytes(target, testKey, tampered, backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest})
	if !errors.Is(err, backup.ErrCorrupt) {
		t.Fatalf("restore of a file with an edited header: %v, want ErrCorrupt", err)
	}
	assertUntouched(t, target, newest)
}

func TestRestoreFailsWhenTheSourceCannotBeOpened(t *testing.T) {
	target := seededTarget(t)
	newest := schemaVersion(t, target)
	boom := errors.New("bucket unreachable")
	_, err := backup.Restore(context.Background(), target, testKey,
		func() (io.ReadCloser, error) { return nil, boom },
		backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest})
	if !errors.Is(err, boom) {
		t.Fatalf("restore: %v, want the open error", err)
	}
	assertUntouched(t, target, newest)
}

func TestIsEmpty(t *testing.T) {
	ctx := context.Background()
	fresh := targetDB(t)
	if empty, err := backup.IsEmpty(ctx, fresh.Pool()); err != nil || !empty {
		t.Errorf("IsEmpty on a database without the schema = %v, %v; want true", empty, err)
	}
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	exec(t, fresh.Pool(), `INSERT INTO instance_ca (sealed) VALUES ('\x01')`)
	if empty, err := backup.IsEmpty(ctx, fresh.Pool()); err != nil || !empty {
		t.Errorf("IsEmpty with only the instance CA = %v, %v; want true", empty, err)
	}
	exec(t, fresh.Pool(), `INSERT INTO organizations (id, slug, name) VALUES (gen_random_uuid(), 'o', 'O')`)
	if empty, err := backup.IsEmpty(ctx, fresh.Pool()); err != nil || empty {
		t.Errorf("IsEmpty with an organization = %v, %v; want false", empty, err)
	}
}
