package checks_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// retentionDB returns a freshly migrated database of its own, connected as
// its owner — a NOSUPERUSER role, the way the manager's migration
// connection is the schema owner — so the tests prove the owner may replace
// the policies, and changing them cannot disturb the shared test database.
func retentionDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

// retentionJob is one table's retention policy as TimescaleDB reports it.
type retentionJob struct {
	id        int64
	dropAfter string
}

// retentionJobs reads every retention policy job of table.
func retentionJobs(t *testing.T, d *db.DB, table string) []retentionJob {
	t.Helper()
	rows, err := d.Pool().Query(ctx, `
		SELECT job_id, config->>'drop_after' FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_schema = 'public' AND hypertable_name = $1
		ORDER BY job_id`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var jobs []retentionJob
	for rows.Next() {
		var j retentionJob
		if err := rows.Scan(&j.id, &j.dropAfter); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return jobs
}

// onlyJob returns table's one retention job, failing on none or several.
func onlyJob(t *testing.T, d *db.DB, table string) retentionJob {
	t.Helper()
	jobs := retentionJobs(t, d, table)
	if len(jobs) != 1 {
		t.Fatalf("%s has %d retention jobs (%v), want exactly one", table, len(jobs), jobs)
	}
	return jobs[0]
}

var retentionTables = []string{"check_samples", "check_state_changes"}

// TestRetentionSyncSQLUnchangedKeepsTheJobs: the migrations set 90 days,
// and syncing the default 90 leaves both jobs exactly as they are — same
// job id, so nothing was removed and re-added.
func TestRetentionSyncSQLUnchangedKeepsTheJobs(t *testing.T) {
	d := retentionDB(t)
	before := map[string]retentionJob{}
	for _, table := range retentionTables {
		before[table] = onlyJob(t, d, table)
		if before[table].dropAfter != "90 days" {
			t.Fatalf("%s after migration drop_after = %q, want 90 days", table, before[table].dropAfter)
		}
	}
	var logs bytes.Buffer
	syncer := checks.NewRetentionSync(checks.NewRetentionPolicySQLStore(d), slog.New(slog.NewTextHandler(&logs, nil)))
	if err := syncer.Sync(ctx, 90); err != nil {
		t.Fatal(err)
	}
	for _, table := range retentionTables {
		if got := onlyJob(t, d, table); got != before[table] {
			t.Errorf("%s job = %+v after an unchanged sync, want %+v", table, got, before[table])
		}
	}
	if logs.Len() != 0 {
		t.Errorf("an unchanged sync logged: %s", logs.String())
	}
}

// TestRetentionSyncSQLSetsBothTablesWhenDifferent: a different maximum
// replaces both policies; syncing it again leaves the new jobs alone.
func TestRetentionSyncSQLSetsBothTablesWhenDifferent(t *testing.T) {
	d := retentionDB(t)
	before := map[string]retentionJob{}
	for _, table := range retentionTables {
		before[table] = onlyJob(t, d, table)
	}
	var logs bytes.Buffer
	syncer := checks.NewRetentionSync(checks.NewRetentionPolicySQLStore(d), slog.New(slog.NewTextHandler(&logs, nil)))
	if err := syncer.Sync(ctx, 30); err != nil {
		t.Fatal(err)
	}
	after := map[string]retentionJob{}
	for _, table := range retentionTables {
		after[table] = onlyJob(t, d, table)
		if after[table].dropAfter != "30 days" {
			t.Errorf("%s drop_after = %q, want 30 days", table, after[table].dropAfter)
		}
		if after[table].id == before[table].id {
			t.Errorf("%s kept job %d, want a replaced policy", table, before[table].id)
		}
		if !strings.Contains(logs.String(), "table="+table) {
			t.Errorf("no log line for %s: %s", table, logs.String())
		}
	}

	if err := syncer.Sync(ctx, 30); err != nil {
		t.Fatal(err)
	}
	for _, table := range retentionTables {
		if got := onlyJob(t, d, table); got != after[table] {
			t.Errorf("%s job = %+v after a repeated sync, want %+v", table, got, after[table])
		}
	}
}

// TestRetentionSyncSQLAddsAMissingPolicy: a policy removed by hand comes
// back at the next start.
func TestRetentionSyncSQLAddsAMissingPolicy(t *testing.T) {
	d := retentionDB(t)
	if _, err := d.Pool().Exec(ctx, `SELECT remove_retention_policy('check_state_changes')`); err != nil {
		t.Fatal(err)
	}
	keep := onlyJob(t, d, "check_samples")
	if err := checks.NewRetentionSync(checks.NewRetentionPolicySQLStore(d), slog.New(slog.DiscardHandler)).Sync(ctx, 90); err != nil {
		t.Fatal(err)
	}
	if got := onlyJob(t, d, "check_state_changes"); got.dropAfter != "90 days" {
		t.Errorf("check_state_changes drop_after = %q, want 90 days", got.dropAfter)
	}
	if got := onlyJob(t, d, "check_samples"); got != keep {
		t.Errorf("check_samples job = %+v, want it untouched (%+v)", got, keep)
	}
}

// TestRetentionSyncSQLConcurrentStarts: several managers starting at once
// with the same new value all succeed, each table ends with exactly one
// policy at that value, and only one of them replaced it.
func TestRetentionSyncSQLConcurrentStarts(t *testing.T) {
	d := retentionDB(t)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	const managers = 8
	errs := make(chan error, managers)
	var wg sync.WaitGroup
	for range managers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- checks.NewRetentionSync(checks.NewRetentionPolicySQLStore(d), log).Sync(ctx, 45)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent sync: %v", err)
		}
	}
	for _, table := range retentionTables {
		if got := onlyJob(t, d, table); got.dropAfter != "45 days" {
			t.Errorf("%s drop_after = %q, want 45 days", table, got.dropAfter)
		}
		if n := strings.Count(logs.String(), "table="+table+" "); n != 1 {
			t.Errorf("%s was replaced %d times, want once: %s", table, n, logs.String())
		}
	}
}
