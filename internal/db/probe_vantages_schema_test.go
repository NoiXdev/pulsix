package db_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestProbeVantageSchema: migration 00098 adds host_agents.probes_supported
// (default false, so existing agents are not given assignments), the three
// probe tables, and the retention policy of the sample hypertable.
func TestProbeVantageSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	var typ, nullable, def string
	if err := d.Pool().QueryRow(ctx, `
		SELECT data_type, is_nullable, column_default FROM information_schema.columns
		WHERE table_name = 'host_agents' AND column_name = 'probes_supported'`).Scan(&typ, &nullable, &def); err != nil {
		t.Fatal(err)
	}
	if typ != "boolean" || nullable != "NO" || def != "false" {
		t.Errorf("probes_supported: %s nullable=%s default=%s", typ, nullable, def)
	}
	var hyper, retention int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.hypertables WHERE hypertable_name = 'probe_samples'`).Scan(&hyper); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_name = 'probe_samples' AND proc_name = 'policy_retention'`).Scan(&retention); err != nil {
		t.Fatal(err)
	}
	if hyper != 1 || retention != 1 {
		t.Errorf("probe_samples hypertable=%d retention policies=%d, want 1 and 1", hyper, retention)
	}
}
