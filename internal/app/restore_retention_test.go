package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
)

// TestRunRestoreAppliesTheMeasurementRetention (backlog 3.15): a restore
// re-runs migrations 00044 and 00046, which set the 90-day policy; the
// restore then sets both policies to the configured maximum, so a restored
// database never waits for the next manager start with the wrong one.
func TestRunRestoreAppliesTheMeasurementRetention(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	dir, name := takeBackup(t, cfg)
	target := restoreTarget(t, cfg)
	target.SampleRetentionDays = 30

	if err := RunRestore(ctx, target, RestoreOptions{From: dir, Name: name}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	owner, err := db.Open(ctx, target.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, table := range []string{"check_samples", "check_state_changes"} {
		if got := retentionDropAfter(t, owner, table); got != "30 days" {
			t.Errorf("%s drop_after = %q after a restore with 30 days, want 30 days", table, got)
		}
	}
}

// fakeRetentionSyncer records the days it was asked for and returns err.
type fakeRetentionSyncer struct {
	days int
	err  error
}

func (f *fakeRetentionSyncer) Sync(_ context.Context, days int) error {
	f.days = days
	return f.err
}

func TestSyncRetentionAfterRestore(t *testing.T) {
	ok := &fakeRetentionSyncer{}
	if err := syncRetentionAfterRestore(context.Background(), ok, 45); err != nil {
		t.Fatalf("a successful sync returned %v", err)
	}
	if ok.days != 45 {
		t.Errorf("synced %d days, want 45", ok.days)
	}

	boom := errors.New("boom")
	err := syncRetentionAfterRestore(context.Background(), &fakeRetentionSyncer{err: boom}, 45)
	if !errors.Is(err, boom) {
		t.Fatalf("a failed sync returned %v, want it to wrap %v", err, boom)
	}
	for _, want := range []string{"data is restored", "45 days", "boom", "Start the manager"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
