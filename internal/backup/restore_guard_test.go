package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/backup"
)

// TestRestoreRechecksManagersBeforeWipe: a manager that connected after the
// command's first check stops the restore through BeforeWipe, before
// anything is dropped. BeforeWipe runs only once the file is verified and
// the database may be replaced.
func TestRestoreRechecksManagersBeforeWipe(t *testing.T) {
	src := sourceDB(t)
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{})
	target := seededTarget(t)
	newest := schemaVersion(t, target)

	managerUp := errors.New("a manager is connected")
	calls := 0
	opts := backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest,
		BeforeWipe: func(context.Context) error { calls++; return managerUp }}
	_, err := restoreBytes(target, testKey, data, opts)
	if !errors.Is(err, managerUp) {
		t.Fatalf("restore with a failing BeforeWipe: %v, want its error", err)
	}
	if errors.Is(err, backup.ErrRestoreIncomplete) {
		t.Errorf("err %v says the database was cleared; nothing was", err)
	}
	if calls != 1 {
		t.Errorf("BeforeWipe ran %d times, want once", calls)
	}
	assertUntouched(t, target, newest)

	// Refusals before it do not reach BeforeWipe.
	calls = 0
	opts.Replace = false
	if _, err := restoreBytes(target, testKey, data, opts); !errors.Is(err, backup.ErrNotEmpty) {
		t.Fatalf("restore into a non-empty database: %v, want ErrNotEmpty", err)
	}
	opts.Replace = true
	if _, err := restoreBytes(target, [32]byte{9}, data, opts); !errors.Is(err, backup.ErrWrongKey) {
		t.Fatalf("restore with another key: %v, want ErrWrongKey", err)
	}
	if calls != 0 {
		t.Errorf("BeforeWipe ran %d times for refused restores, want never", calls)
	}
	assertUntouched(t, target, newest)
}

// TestLoadRefusesNonEmptyCoreTables: the freshly migrated schema has no
// users, organizations or memberships. A row there before the load was
// written by something else (a manager that started meanwhile); the load
// refuses instead of merging the backup into it.
func TestLoadRefusesNonEmptyCoreTables(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{})
	newest := schemaVersion(t, src)

	for _, tc := range []struct {
		table, insert string
	}{
		{"users", `INSERT INTO users (id, email, display_name) VALUES ('00000000-0000-4000-8000-0000000000aa', 'intruder@example.com', 'Intruder')`},
		{"organizations", `INSERT INTO organizations (id, slug, name) VALUES ('00000000-0000-4000-8000-0000000000ab', 'intruder', 'Intruder')`},
	} {
		t.Run(tc.table, func(t *testing.T) {
			target := targetDB(t)
			opts := backup.RestoreOptions{BinarySchemaVersion: newest}
			backup.SetAfterMigrate(&opts, func(p *pgxpool.Pool) error {
				_, err := p.Exec(context.Background(), tc.insert)
				return err
			})
			_, err := restoreBytes(target, testKey, data, opts)
			if !errors.Is(err, backup.ErrRestoreIncomplete) || !errors.Is(err, backup.ErrWrittenDuringRestore) {
				t.Fatalf("restore over a row in %s: %v, want ErrRestoreIncomplete and ErrWrittenDuringRestore", tc.table, err)
			}
			if !strings.Contains(err.Error(), tc.table) {
				t.Errorf("err %q does not name %s", err, tc.table)
			}
			if n := rowCount(t, target.Pool(), tc.table); n != 1 {
				t.Errorf("%s has %d rows after the refused load, want only the intruder", tc.table, n)
			}
			if n := rowCount(t, target.Pool(), "hosts"); n != 0 {
				t.Errorf("hosts has %d rows after the refused load, want none merged", n)
			}
		})
	}
}

// TestRestoreEndsAllSessions: a restore signs everybody out. The backup's
// sessions belong to a past state of the instance.
func TestRestoreEndsAllSessions(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	if n := rowCount(t, src.Pool(), "sessions"); n == 0 {
		t.Fatal("the fixture has no session")
	}
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{})

	target := targetDB(t)
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n := rowCount(t, target.Pool(), "sessions"); n != 0 {
		t.Errorf("%d sessions after the restore, want none", n)
	}
	if n := rowCount(t, target.Pool(), "users"); n != 1 {
		t.Errorf("%d users after the restore, want the backup's one", n)
	}
}
