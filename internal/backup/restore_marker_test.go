package backup_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/restoremark"
)

// readRestoreMarker decodes the restore row of instance_settings.
func readRestoreMarker(t *testing.T, pool *pgxpool.Pool) restoremark.Marker {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT value FROM instance_settings WHERE key = $1`, restoremark.Key).Scan(&raw); err != nil {
		t.Fatalf("read the restore marker: %v", err)
	}
	var m restoremark.Marker
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode the restore marker %s: %v", raw, err)
	}
	return m
}

// TestRestoreWritesRestoreMarker: a restore records which backup it restored
// and when, so the manager can tell certificates issued between the two from
// those it must not adopt. The load writes it fresh; a marker the backup
// itself carried (a restore that was itself backed up) never leaks through.
func TestRestoreWritesRestoreMarker(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	stale := restoremark.Marker{
		BackupCreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		RestoredAt:      time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	staleJSON, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Pool().Exec(context.Background(),
		`INSERT INTO instance_settings (key, value) VALUES ($1, $2)`, restoremark.Key, staleJSON); err != nil {
		t.Fatal(err)
	}
	data, h := dumpBytes(t, src.Pool(), backup.DumpOptions{})
	newest := schemaVersion(t, src)

	target := targetDB(t)
	before := time.Now()
	if _, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: newest}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	first := readRestoreMarker(t, target.Pool())
	if !first.BackupCreatedAt.Equal(h.CreatedAt) {
		t.Errorf("marker backup_created_at %v, want the backup header's %v", first.BackupCreatedAt, h.CreatedAt)
	}
	if first.RestoredAt.Before(before.Add(-5*time.Second)) || first.RestoredAt.After(time.Now().Add(5*time.Second)) {
		t.Errorf("marker restored_at %v is not within seconds of now (%v)", first.RestoredAt, before)
	}
	if first.BackupCreatedAt.Equal(stale.BackupCreatedAt) || first.RestoredAt.Equal(stale.RestoredAt) {
		t.Errorf("the backup's own restore marker %+v leaked through: %+v", stale, first)
	}
	if n := rowCount(t, target.Pool(), "instance_settings"); n != rowCount(t, src.Pool(), "instance_settings") {
		t.Errorf("%d instance_settings rows after the restore, want the source's %d (the marker replaces, not adds)",
			n, rowCount(t, src.Pool(), "instance_settings"))
	}

	// A second restore replaces the marker.
	time.Sleep(20 * time.Millisecond)
	data2, h2 := dumpBytes(t, target.Pool(), backup.DumpOptions{})
	if _, err := restoreBytes(target, testKey, data2, backup.RestoreOptions{Replace: true, BinarySchemaVersion: newest}); err != nil {
		t.Fatalf("second restore: %v", err)
	}
	second := readRestoreMarker(t, target.Pool())
	if !second.BackupCreatedAt.Equal(h2.CreatedAt) || second.BackupCreatedAt.Equal(first.BackupCreatedAt) {
		t.Errorf("second marker backup_created_at %v, want the second backup's %v", second.BackupCreatedAt, h2.CreatedAt)
	}
	if !second.RestoredAt.After(first.RestoredAt) {
		t.Errorf("second marker restored_at %v is not after the first's %v", second.RestoredAt, first.RestoredAt)
	}
}
