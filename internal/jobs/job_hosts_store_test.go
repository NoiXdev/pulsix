package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// jobHostLink is one job_hosts row as the tests compare it.
type jobHostLink struct {
	job, host uuid.UUID
	at        time.Time
}

// jobHostLinks returns every job_hosts row of org, read through q (an
// unscoped pulsix_app or superuser pool: the bypass policy shows them all).
func jobHostLinks(t *testing.T, q gen.DBTX, org uuid.UUID) []jobHostLink {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT job_id, host_id, created_at FROM job_hosts WHERE org_id = $1 ORDER BY job_id, host_id`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []jobHostLink
	for rows.Next() {
		var l jobHostLink
		if err := rows.Scan(&l.job, &l.host, &l.at); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// jobCreatedAt reads the job's own created_at, which its links copy.
func jobCreatedAt(t *testing.T, d *db.DB, id uuid.UUID) time.Time {
	t.Helper()
	var at time.Time
	if err := d.Pool().QueryRow(context.Background(), `SELECT created_at FROM jobs WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// superuser opens the bootstrap superuser on the shared test database, for
// the writes pulsix_app may not make and RLS would otherwise hide.
func superuser(t *testing.T) *db.DB {
	t.Helper()
	owner, err := db.Open(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	return owner
}

// TestCreateJobLinksItsHosts (backlog 7.84): the insert that creates a job
// links it to each distinct host it targets, with the job's created_at, and
// skips an id that names no host; a job without hosts has no links.
func TestCreateJobLinksItsHosts(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-link-org")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	hostA, hostB := seedHost(t, d, org, zone, "a"), seedHost(t, d, org, zone, "b")

	j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	j.HostIDs = []uuid.UUID{hostA, hostA, hostB, uuid.Must(uuid.NewV7())}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	none := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	none.HostIDs = nil
	if err := store.CreateJob(scoped, none, nil); err != nil {
		t.Fatalf("CreateJob without hosts: %v", err)
	}

	at := jobCreatedAt(t, d, j.ID)
	want := map[uuid.UUID]bool{hostA: true, hostB: true}
	got := jobHostLinks(t, d.Pool(), org)
	if len(got) != len(want) {
		t.Fatalf("links = %v, want one per host of %s: %v", got, j.ID, want)
	}
	for _, l := range got {
		if l.job != j.ID || !want[l.host] || !l.at.Equal(at) {
			t.Fatalf("link %v, want job %s, a host of %v, created_at %s", l, j.ID, want, at)
		}
		delete(want, l.host)
	}
}

// TestListJobsByHostStaysInItsOrganization (backlog 7.84): another
// organization listing a host's runs sees none of them, and a link can
// never pair a job of one organization with a host of another, not even
// when written past RLS.
func TestListJobsByHostStaysInItsOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	orgA, orgB := seedOrg(t, q, "jobs-link-tenancy-a"), seedOrg(t, q, "jobs-link-tenancy-b")
	store := jobs.NewSQLStore(d)
	zoneA, zoneB := seedZone(t, d, orgA), seedZone(t, d, orgB)
	hostA := seedHost(t, d, orgA, zoneA, "a")

	jobA := newJob(orgA, zoneA, jobs.KindRun, jobs.StatusQueued)
	jobA.HostIDs = []uuid.UUID{hostA}
	if err := store.CreateJob(db.WithOrgScope(ctx, orgA), jobA, nil); err != nil {
		t.Fatal(err)
	}
	jobB := newJob(orgB, zoneB, jobs.KindRun, jobs.StatusQueued)
	jobB.HostIDs = []uuid.UUID{hostA}
	if err := store.CreateJob(db.WithOrgScope(ctx, orgB), jobB, nil); err != nil {
		t.Fatal(err)
	}

	scopedB := db.WithOrgScope(ctx, orgB)
	for _, org := range []uuid.UUID{orgB, orgA} {
		got, _, err := store.ListJobs(scopedB, org, jobs.Filter{HostID: &hostA}, "", 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("org B's scope listing org A's host (org arg %s) = %v, %v; want nothing", org, got, err)
		}
	}
	if links := jobHostLinks(t, d.Pool(), orgB); len(links) != 0 {
		t.Fatalf("org B's job linked to org A's host: %v", links)
	}

	_, err := superuser(t).Pool().Exec(ctx,
		`INSERT INTO job_hosts (org_id, job_id, host_id, created_at) VALUES ($1, $2, $3, now())`, orgB, jobB.ID, hostA)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("linking org B's job to org A's host = %v, want a foreign key violation", err)
	}
}

// TestJobHostsFollowTheirJobAndHost (backlog 7.84): deleting a host drops
// its links but keeps the runs (and their host_ids, the run view's record),
// deleting a job drops its links, and deleting the organization leaves
// none of its links behind.
func TestJobHostsFollowTheirJobAndHost(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-link-cascade")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	hostA, hostB := seedHost(t, d, org, zone, "a"), seedHost(t, d, org, zone, "b")

	both := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	both.HostIDs = []uuid.UUID{hostA, hostB}
	onlyB := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	onlyB.HostIDs = []uuid.UUID{hostB}
	for _, j := range []jobs.Job{both, onlyB} {
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := gen.New(d).DeleteHost(scoped, gen.DeleteHostParams{ID: hostA, OrgID: org}); err != nil || n != 1 {
		t.Fatalf("DeleteHost = %d, %v", n, err)
	}
	for _, l := range jobHostLinks(t, d.Pool(), org) {
		if l.host == hostA {
			t.Fatalf("link %v survived its host's deletion", l)
		}
	}
	if n := len(jobHostLinks(t, d.Pool(), org)); n != 2 {
		t.Fatalf("links after deleting host A = %d, want 2 (both runs to host B)", n)
	}
	kept, err := store.GetJob(scoped, org, both.ID)
	if err != nil || len(kept.HostIDs) != 2 || kept.HostIDs[0] != hostA {
		t.Fatalf("run after deleting its host = %+v, %v; want it kept with host_ids %v", kept, err, both.HostIDs)
	}

	owner := superuser(t)
	if _, err := owner.Pool().Exec(ctx, `DELETE FROM jobs WHERE id = $1`, both.ID); err != nil {
		t.Fatal(err)
	}
	if links := jobHostLinks(t, d.Pool(), org); len(links) != 1 || links[0].job != onlyB.ID {
		t.Fatalf("links after deleting a job = %v, want only %s's", links, onlyB.ID)
	}

	if _, err := owner.Pool().Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if links := jobHostLinks(t, owner.Pool(), org); len(links) != 0 {
		t.Fatalf("links after deleting the organization = %v, want none", links)
	}
}
