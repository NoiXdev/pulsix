package jobs_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/paging"
)

// TestSQLStoreCheckMode: the job row keeps check_mode, and the check mode
// filter narrows every list and count query (cursor and page mode, sorted,
// one host's runs, the live runs). Runs, oldest first, all on host x:
//
//	idx check  status
//	0   no     succeeded
//	1   yes    succeeded
//	2   yes    running
//	3   no     running
func TestSQLStoreCheckMode(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-check-mode")
	scoped := db.WithOrgScope(ctx, org)
	store := jobs.NewSQLStore(d)
	zone := seedZone(t, d, org)
	host := seedHost(t, d, org, zone, "x")

	base := time.Now().Add(-time.Hour).Truncate(time.Minute)
	var ids []uuid.UUID
	for i, spec := range []struct {
		check  bool
		status jobs.Status
	}{{false, jobs.StatusSucceeded}, {true, jobs.StatusSucceeded}, {true, jobs.StatusRunning}, {false, jobs.StatusRunning}} {
		j := newJob(org, zone, jobs.KindRun, spec.status)
		j.HostIDs, j.CheckMode = []uuid.UUID{host}, spec.check
		j.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	for i, want := range []bool{false, true, true, false} {
		got, err := store.GetJob(scoped, org, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if got.CheckMode != want {
			t.Fatalf("run %d: check mode %v, want %v", i, got.CheckMode, want)
		}
	}

	idx := func(t *testing.T, got []jobs.Job) []int {
		t.Helper()
		out := make([]int, len(got))
		for i, j := range got {
			out[i] = slices.Index(ids, j.ID)
		}
		return out
	}
	yes, no := true, false
	page := paging.Page{Number: 1, Size: 10, Sort: jobs.SortCreated, Desc: true}
	sorted := paging.Page{Number: 1, Size: 10, Sort: jobs.SortStatus}
	for _, tc := range []struct {
		name  string
		check *bool
		host  bool
		live  bool
		want  []int
	}{
		{"both", nil, false, false, []int{3, 2, 1, 0}},
		{"dry runs", &yes, false, false, []int{2, 1}},
		{"real runs", &no, false, false, []int{3, 0}},
		{"dry runs of the host", &yes, true, false, []int{2, 1}},
		{"real runs of the host", &no, true, false, []int{3, 0}},
		{"live dry runs", &yes, false, true, []int{2}},
		{"live real runs of the host", &no, true, true, []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := jobs.Filter{CheckMode: tc.check, Active: tc.live}
			if tc.host {
				f.HostID = &host
			}
			cursor, _, err := store.ListJobs(scoped, org, f, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := idx(t, cursor); !slices.Equal(got, tc.want) {
				t.Fatalf("cursor mode = %v, want %v", got, tc.want)
			}
			for _, p := range []paging.Page{page, sorted} {
				got, total, err := store.ListJobsPage(scoped, org, f, p)
				if err != nil {
					t.Fatal(err)
				}
				gotIdx := idx(t, got)
				slices.Sort(gotIdx)
				want := slices.Sorted(slices.Values(tc.want))
				if !slices.Equal(gotIdx, want) || total != len(tc.want) {
					t.Fatalf("page %+v = %v (total %d), want %v", p, gotIdx, total, want)
				}
			}
		})
	}
}

// TestSQLStoreDryRun: a dry run's options round-trip through jobs.dry_run
// (the detail and the lists read them); an ordinary run stores NULL.
func TestSQLStoreDryRun(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-dry-run")
	scoped := db.WithOrgScope(ctx, org)
	store := jobs.NewSQLStore(d)
	zone := seedZone(t, d, org)

	dry := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	dry.CheckMode, dry.DryRun = true, &jobs.DryRun{Tags: []string{"web", "db"}, Diff: true, Draft: true}
	ordinary := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	ordinary.CreatedAt = dry.CreatedAt.Add(time.Second)
	for _, j := range []jobs.Job{dry, ordinary} {
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
	}

	var raw []byte
	if err := d.Pool().QueryRow(scoped, `SELECT dry_run FROM jobs WHERE id = $1`, ordinary.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Fatalf("an ordinary run stores dry_run %s, want NULL", raw)
	}

	check := func(t *testing.T, got jobs.Job) {
		t.Helper()
		switch got.ID {
		case dry.ID:
			if got.DryRun == nil || !slices.Equal(got.DryRun.Tags, []string{"web", "db"}) || !got.DryRun.Diff || !got.DryRun.Draft {
				t.Fatalf("dry run = %+v", got.DryRun)
			}
		case ordinary.ID:
			if got.DryRun != nil {
				t.Fatalf("ordinary run has dry run %+v", got.DryRun)
			}
		}
	}
	for _, id := range []uuid.UUID{dry.ID, ordinary.ID} {
		got, err := store.GetJob(scoped, org, id)
		if err != nil {
			t.Fatal(err)
		}
		check(t, got)
	}
	listed, _, err := store.ListJobs(scoped, org, jobs.Filter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	paged, _, err := store.ListJobsPage(scoped, org, jobs.Filter{}, paging.Page{Number: 1, Size: 10, Sort: jobs.SortCreated, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || len(paged) != 2 {
		t.Fatalf("listed %d and paged %d jobs, want 2 each", len(listed), len(paged))
	}
	for _, j := range append(listed, paged...) {
		check(t, j)
	}
}
