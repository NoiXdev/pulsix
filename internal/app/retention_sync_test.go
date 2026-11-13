package app

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// retentionDropAfter reads table's retention policy drop_after.
func retentionDropAfter(t *testing.T, d *db.DB, table string) string {
	t.Helper()
	var dropAfter string
	if err := d.Pool().QueryRow(context.Background(), `
		SELECT config->>'drop_after' FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_schema = 'public' AND hypertable_name = $1`,
		table).Scan(&dropAfter); err != nil {
		t.Fatalf("read %s retention policy: %v", table, err)
	}
	return dropAfter
}

// TestBuildSyncsTheMeasurementRetention (backlog 3.15): Build sets both
// hypertables' retention policy to PULSIX_SAMPLE_RETENTION right after the
// migrations, through the migration connection (here the database's
// NOSUPERUSER owner). Its own fresh database keeps the change away from the
// shared one.
func TestBuildSyncsTheMeasurementRetention(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.DatabaseURL = testdb.FreshDatabase(t)
	cfg.SampleRetentionDays = 30

	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()

	owner, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, table := range []string{"check_samples", "check_state_changes"} {
		if got := retentionDropAfter(t, owner, table); got != "30 days" {
			t.Errorf("%s drop_after = %q after Build with 30 days, want 30 days", table, got)
		}
	}
}
