package backup_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestRestoreMarksRunningAttemptsAbandoned: the backup that takes a
// snapshot is itself a running attempt in it. After a restore nothing is
// running that row, so it must not block "Back up now" there.
func TestRestoreMarksRunningAttemptsAbandoned(t *testing.T) {
	src := sourceDB(t)
	exec(t, src.Pool(), `
		INSERT INTO backup_runs (id, kind, started_at, state, object_name) VALUES
			('00000000-0000-4000-8000-0000000000e1', 'manual', now(), 'running', 'pulsix-20261102T030000Z-manual.pxbak');
		INSERT INTO backup_runs (id, kind, slot, started_at, finished_at, state, object_name, size_bytes) VALUES
			('00000000-0000-4000-8000-0000000000e2', 'scheduled', '2026-11-01T03:00:00Z', '2026-11-01T03:00:00Z', '2026-11-01T03:01:00Z', 'succeeded', 'pulsix-20261101T030000Z-scheduled.pxbak', 42);`)
	data, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{})

	target := targetDB(t)
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	attempts, err := backup.NewSQLStore(target).RecentAttempts(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	byState := map[string]backup.Attempt{}
	for _, a := range attempts {
		byState[a.State] = a
	}
	if len(attempts) != 2 {
		t.Fatalf("restored attempts = %+v", attempts)
	}
	if a, ok := byState[backup.StateAbandoned]; !ok || a.FinishedAt == nil || a.Error == "" {
		t.Errorf("running attempt after restore = %+v, want abandoned, finished, with a reason", attempts)
	}
	if a, ok := byState[backup.StateSucceeded]; !ok || a.Size != 42 {
		t.Errorf("finished attempt after restore = %+v, want it unchanged", attempts)
	}
}

// TestDumpThroughAppRoleMatchesOwner: scheduled backups dump through
// pulsix_app without an organization scope. A row-level security policy
// that hid rows from that session would silently shrink every scheduled
// backup; this compares its row counts with the owner's, table by table.
func TestDumpThroughAppRoleMatchesOwner(t *testing.T) {
	owner := sourceDB(t)
	fillEveryTable(t, owner.Pool())
	app := testdb.Open(t) // pulsix_app, no organization scope

	opts := backup.DumpOptions{IncludeMetrics: true, Now: func() time.Time { return time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC) }}
	ownerData, _ := dumpBytes(t, owner.Pool(), opts)
	appData, _ := dumpBytes(t, app.Pool(), opts)

	ownerManifest, err := backup.Verify(bytes.NewReader(ownerData), testKey)
	if err != nil {
		t.Fatal(err)
	}
	appManifest, err := backup.Verify(bytes.NewReader(appData), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(appManifest.Tables, ownerManifest.Tables) {
		for i := range ownerManifest.Tables {
			if i >= len(appManifest.Tables) || appManifest.Tables[i] != ownerManifest.Tables[i] {
				t.Errorf("pulsix_app dumped %v, the owner %v", tableAt(appManifest.Tables, i), ownerManifest.Tables[i])
			}
		}
		t.Fatalf("row counts differ between pulsix_app and the owner")
	}
	if !slices.Equal(appManifest.Sequences, ownerManifest.Sequences) {
		t.Errorf("sequences: pulsix_app %v, owner %v", appManifest.Sequences, ownerManifest.Sequences)
	}
	for _, tc := range ownerManifest.Tables {
		if tc.Rows == 0 {
			t.Errorf("%s dumped empty: the fixture fills every table", tc.Name)
		}
	}
}

func tableAt(tables []backup.TableCount, i int) any {
	if i < len(tables) {
		return tables[i]
	}
	return "nothing"
}
