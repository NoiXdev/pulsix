package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/db"
)

// oldBackup returns a backup of a database at schema 60 holding one
// organization, and the newest schema version.
func oldBackup(t *testing.T) (data []byte, newest int64) {
	t.Helper()
	ctx := context.Background()
	newest, err := db.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	old := targetDB(t)
	if err := old.MigrateTo(ctx, 60); err != nil {
		t.Fatal(err)
	}
	exec(t, old.Pool(), `INSERT INTO organizations (id, slug, name) VALUES ('00000000-0000-4000-8000-0000000000c1', 'old-org', 'Old')`)
	data, h := dumpBytes(t, old.Pool(), backup.DumpOptions{IncludeMetrics: true})
	if h.SchemaVersion != 60 {
		t.Fatalf("dump of a database at 60 says schema %d", h.SchemaVersion)
	}
	return data, newest
}

func organizationCount(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('public.organizations') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return 0
	}
	return rowCount(t, pool, "organizations")
}

// TestRestoreRefusesASchemaMigratedPastTheBackup: a manager that migrates
// the wiped schema to the newest version before MigrateTo runs must not
// make the backup's rows skip the migrations in between. The restore stops
// as incomplete, the database holds no data, and the same restore succeeds
// when run again.
func TestRestoreRefusesASchemaMigratedPastTheBackup(t *testing.T) {
	ctx := context.Background()
	data, newest := oldBackup(t)
	target := seededTarget(t)

	opts := backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest}
	backup.SetAfterWipe(&opts, func(*pgxpool.Pool) error { return target.Migrate(ctx) })
	m, err := restoreBytes(target, testKey, data, opts)
	if !errors.Is(err, backup.ErrRestoreIncomplete) {
		t.Fatalf("restore with the schema migrated underneath: %v, want ErrRestoreIncomplete", err)
	}
	if errors.Is(err, backup.ErrNotMigrated) {
		t.Errorf("err %v says the data was loaded; nothing was", err)
	}
	if !strings.Contains(err.Error(), "60") || !strings.Contains(err.Error(), "migrated") {
		t.Errorf("err %q does not explain the schema version mismatch", err)
	}
	if m.Header.SchemaVersion != 60 {
		t.Errorf("an incomplete restore returns the manifest; schema %d, want 60", m.Header.SchemaVersion)
	}
	if n := organizationCount(t, target.Pool()); n != 0 {
		t.Errorf("%d organizations after the refused load, want none", n)
	}
	if empty, err := backup.IsEmpty(ctx, target.Pool()); err != nil || !empty {
		t.Fatalf("IsEmpty after an incomplete restore = %v, %v; want empty", empty, err)
	}

	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: newest}); err != nil {
		t.Fatalf("running the restore again: %v", err)
	}
	if n := organizationCount(t, target.Pool()); n != 1 {
		t.Errorf("%d organizations after the second restore, want 1", n)
	}
}

// TestRestoreFailureAfterTheWipeIsIncomplete: a failure between the wipe and
// the commit of the load is ErrRestoreIncomplete; a failure before the wipe
// is not.
func TestRestoreFailureAfterTheWipeIsIncomplete(t *testing.T) {
	data, newest := oldBackup(t)
	target := seededTarget(t)

	boom := errors.New("boom")
	opts := backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest}
	backup.SetAfterMigrate(&opts, func(*pgxpool.Pool) error { return boom })
	_, err := restoreBytes(target, testKey, data, opts)
	if !errors.Is(err, backup.ErrRestoreIncomplete) || !errors.Is(err, boom) {
		t.Fatalf("failure after the wipe: %v, want ErrRestoreIncomplete wrapping the cause", err)
	}

	before := seededTarget(t)
	_, err = restoreBytes(before, [32]byte{9}, data, backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest})
	if !errors.Is(err, backup.ErrWrongKey) || errors.Is(err, backup.ErrRestoreIncomplete) {
		t.Fatalf("failure before the wipe: %v, want ErrWrongKey and not ErrRestoreIncomplete", err)
	}
}

// TestRestoreMigrationFailureAfterTheLoad: the rows are in, but the
// migrations past the backup's version fail. That is ErrRestoreIncomplete
// and ErrNotMigrated, and the data stays loaded.
func TestRestoreMigrationFailureAfterTheLoad(t *testing.T) {
	data, newest := oldBackup(t)
	target := targetDB(t)

	opts := backup.RestoreOptions{BinarySchemaVersion: newest}
	// Migration 61 creates shared_job_templates; a table of that name in
	// the way makes it fail.
	backup.SetAfterMigrate(&opts, func(p *pgxpool.Pool) error {
		_, err := p.Exec(context.Background(), `CREATE TABLE shared_job_templates (id int)`)
		return err
	})
	_, err := restoreBytes(target, testKey, data, opts)
	if !errors.Is(err, backup.ErrNotMigrated) || !errors.Is(err, backup.ErrRestoreIncomplete) {
		t.Fatalf("migration failure after the load: %v, want ErrNotMigrated and ErrRestoreIncomplete", err)
	}
	if n := organizationCount(t, target.Pool()); n != 1 {
		t.Errorf("%d organizations after the load, want the backup's 1", n)
	}
}
