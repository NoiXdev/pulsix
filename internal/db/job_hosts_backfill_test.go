package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestJobHostsBackfill (backlog 7.84): migration 00082 links every run of
// the history to each distinct host it targeted that still exists, with the
// run's own created_at, skips ids that name no host, and swaps the GIN
// index of 00079 for the link table's host index.
func TestJobHostsBackfill(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 81); err != nil {
		t.Fatal(err)
	}

	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostA, hostB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	job1, job2, job3 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ghost := uuid.Must(uuid.NewV7())
	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	created := map[uuid.UUID]time.Time{job1: day, job2: day.Add(time.Hour), job3: day.Add(2 * time.Hour)}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'backfill', 'backfill')`, []any{org}},
		{`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, []any{zone, org}},
		{`INSERT INTO hosts (id, org_id, zone_id, name) VALUES ($1, $3, $4, 'a'), ($2, $3, $4, 'b')`, []any{hostA, hostB, org, zone}},
		{`INSERT INTO jobs (id, org_id, zone_id, kind, host_ids, run_group_id, created_at) VALUES
		    ($1, $4, $5, 'run', $6::uuid[], gen_random_uuid(), $9),
		    ($2, $4, $5, 'run', $7::uuid[], gen_random_uuid(), $10),
		    ($3, $4, $5, 'run', $8::uuid[], gen_random_uuid(), $11)`,
			[]any{job1, job2, job3, org, zone,
				[]uuid.UUID{hostA, hostB}, []uuid.UUID{hostA, ghost, hostA}, []uuid.UUID{},
				created[job1], created[job2], created[job3]}},
	} {
		if _, err := d.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%.40s: %v", stmt.sql, err)
		}
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	type link struct{ job, host uuid.UUID }
	want := map[link]bool{{job1, hostA}: true, {job1, hostB}: true, {job2, hostA}: true}
	rows, err := d.Pool().Query(ctx, `SELECT org_id, job_id, host_id, created_at FROM job_hosts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[link]bool{}
	for rows.Next() {
		var l link
		var orgID uuid.UUID
		var at time.Time
		if err := rows.Scan(&orgID, &l.job, &l.host, &at); err != nil {
			t.Fatal(err)
		}
		if got[l] {
			t.Errorf("link %v twice", l)
		}
		got[l] = true
		if orgID != org {
			t.Errorf("link %v org = %s, want %s", l, orgID, org)
		}
		if !at.Equal(created[l.job]) {
			t.Errorf("link %v created_at = %s, want its job's %s", l, at, created[l.job])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("job_hosts = %v, want %v", got, want)
	}
	for l := range want {
		if !got[l] {
			t.Fatalf("job_hosts = %v, want %v", got, want)
		}
	}

	indexes := map[string]bool{}
	irows, err := d.Pool().Query(ctx,
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND indexname IN ('job_hosts_host_idx', 'jobs_host_ids_idx')`)
	if err != nil {
		t.Fatal(err)
	}
	defer irows.Close()
	for irows.Next() {
		var name string
		if err := irows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	if err := irows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexes["job_hosts_host_idx"] || indexes["jobs_host_ids_idx"] {
		t.Fatalf("indexes = %v, want job_hosts_host_idx and no jobs_host_ids_idx", indexes)
	}
}
