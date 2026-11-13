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
	target.SampleRetentionSet = true

	var stdout bytes.Buffer
	if err := RunRestore(ctx, target, RestoreOptions{From: dir, Name: name}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(stdout.String(), "Measurement retention: 30 days (PULSIX_SAMPLE_RETENTION).") {
		t.Errorf("restore output %q does not name the applied retention", stdout.String())
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

// TestPrintRetentionApplied: the restore says which retention it applied,
// and says so plainly when it is the default because the variable is not
// set in the restore's own environment.
func TestPrintRetentionApplied(t *testing.T) {
	var set bytes.Buffer
	printRetentionApplied(&set, 365, true)
	if got := set.String(); got != "Measurement retention: 365 days (PULSIX_SAMPLE_RETENTION).\n" {
		t.Errorf("configured = %q", got)
	}
	var unset bytes.Buffer
	printRetentionApplied(&unset, 90, false)
	for _, want := range []string{"Measurement retention: 90 days", "default", "PULSIX_SAMPLE_RETENTION is not set", "older than 90 days"} {
		if !strings.Contains(unset.String(), want) {
			t.Errorf("default = %q, lacks %q", unset.String(), want)
		}
	}
}
