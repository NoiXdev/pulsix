package restoremark_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/restoremark"
)

var (
	backupAt   = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	restoredAt = time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
)

func marker(t *testing.T, m restoremark.Marker) []byte {
	t.Helper()
	value, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestParse: a marker with both times, restore after backup, is a window;
// every other marker is none, and JSON that does not decode is an error.
func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		value []byte
		want  *restoremark.Window
	}{
		{"usable", marker(t, restoremark.Marker{BackupCreatedAt: backupAt, RestoredAt: restoredAt}),
			&restoremark.Window{BackupCreatedAt: backupAt, RestoredAt: restoredAt}},
		{"without a backup time", marker(t, restoremark.Marker{RestoredAt: restoredAt}), nil},
		{"without a restore time", marker(t, restoremark.Marker{BackupCreatedAt: backupAt}), nil},
		{"restored before its backup", marker(t, restoremark.Marker{BackupCreatedAt: restoredAt, RestoredAt: backupAt}), nil},
		{"restored at its backup", marker(t, restoremark.Marker{BackupCreatedAt: backupAt, RestoredAt: backupAt}), nil},
		{"empty object", []byte(`{}`), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := restoremark.Parse(tt.value)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
	if _, err := restoremark.Parse([]byte(`{"backup_created_at": 42`)); err == nil {
		t.Fatal("Parse of corrupt JSON: nil error, want one")
	}
}

// TestWindowContains: strictly between the backup and the restore; a nil
// window contains nothing.
func TestWindowContains(t *testing.T) {
	w := &restoremark.Window{BackupCreatedAt: backupAt, RestoredAt: restoredAt}
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before the backup", backupAt.Add(-time.Second), false},
		{"at the backup", backupAt, false},
		{"inside", backupAt.Add(30 * time.Minute), true},
		{"at the restore", restoredAt, false},
		{"after the restore", restoredAt.Add(time.Second), false},
	}
	for _, tt := range tests {
		if got := w.Contains(tt.at); got != tt.want {
			t.Errorf("%s: Contains = %v, want %v", tt.name, got, tt.want)
		}
	}
	var none *restoremark.Window
	if none.Contains(backupAt.Add(30 * time.Minute)) {
		t.Error("a nil window contains a time")
	}
}

type fakeSettings struct {
	key   string
	value []byte
	err   error
}

func (f *fakeSettings) GetInstanceSetting(_ context.Context, key string) (gen.InstanceSetting, error) {
	f.key = key
	if f.err != nil {
		return gen.InstanceSetting{}, f.err
	}
	return gen.InstanceSetting{Key: key, Value: f.value}, nil
}

// TestRead: the marker row under Key; no row is no window, a fault and a
// corrupt row are errors.
func TestRead(t *testing.T) {
	ctx := context.Background()
	s := &fakeSettings{value: marker(t, restoremark.Marker{BackupCreatedAt: backupAt, RestoredAt: restoredAt})}
	w, err := restoremark.Read(ctx, s)
	if err != nil || w == nil || !w.BackupCreatedAt.Equal(backupAt) || !w.RestoredAt.Equal(restoredAt) || s.key != restoremark.Key {
		t.Fatalf("Read = %+v, %v (key %q)", w, err, s.key)
	}
	if w, err := restoremark.Read(ctx, &fakeSettings{err: pgx.ErrNoRows}); w != nil || err != nil {
		t.Fatalf("Read without a row = %+v, %v; want no window", w, err)
	}
	fault := errors.New("db down")
	if _, err := restoremark.Read(ctx, &fakeSettings{err: fault}); !errors.Is(err, fault) {
		t.Fatalf("Read on a fault = %v, want it wrapped", err)
	}
	if _, err := restoremark.Read(ctx, &fakeSettings{value: []byte(`not json`)}); err == nil {
		t.Fatal("Read of a corrupt row: nil error, want one")
	}
}
