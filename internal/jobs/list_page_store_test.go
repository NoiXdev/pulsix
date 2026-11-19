package jobs_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/paging"
)

// listPageWorld is one organization with six runs of distinct created_at
// (a minute apart, oldest first: index 0 is the oldest) and a second
// organization with a run of its own.
//
//	idx kind   status     trigger  template  hosts
//	0   run    succeeded  manual   tmplP     X
//	1   adhoc  failed     system   -         X
//	2   run    queued     schedule tmplP     Y
//	3   adhoc  running    manual   -         X, Y
//	4   run    failed     webhook  tmplQ     Y
//	5   run    running    manual   tmplQ     X
type listPageWorld struct {
	store        *jobs.SQLStore
	org          uuid.UUID
	ctx          context.Context
	hostX, hostY uuid.UUID
	playbookP    uuid.UUID
	ids          []uuid.UUID // by index above
	otherOrgJob  uuid.UUID
}

func newListPageWorld(t *testing.T, slug string) *listPageWorld {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org, other := seedOrg(t, q, slug), seedOrg(t, q, slug+"-other")
	w := &listPageWorld{store: jobs.NewSQLStore(d), org: org, ctx: db.WithOrgScope(ctx, org)}
	zone := seedZone(t, d, org)
	w.hostX, w.hostY = seedHost(t, d, org, zone, "x"), seedHost(t, d, org, zone, "y")

	tmpl := func(name string) (playbook, template uuid.UUID) {
		playbook, template = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		if _, err := d.Exec(w.ctx, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, 'x', 'editor')`, playbook, org); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(w.ctx, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, $3, $4, '{}')`,
			template, org, name, playbook); err != nil {
			t.Fatal(err)
		}
		return playbook, template
	}
	var tmplP, tmplQ uuid.UUID
	w.playbookP, tmplP = tmpl("p")
	_, tmplQ = tmpl("q")

	base := time.Now().Add(-time.Hour).Truncate(time.Minute)
	specs := []struct {
		kind    jobs.Kind
		status  jobs.Status
		trigger string
		tmpl    *uuid.UUID
		hosts   []uuid.UUID
	}{
		{jobs.KindRun, jobs.StatusSucceeded, jobs.TriggerManual, &tmplP, []uuid.UUID{w.hostX}},
		{jobs.KindAdhoc, jobs.StatusFailed, jobs.TriggerSystem, nil, []uuid.UUID{w.hostX}},
		{jobs.KindRun, jobs.StatusQueued, "schedule", &tmplP, []uuid.UUID{w.hostY}},
		{jobs.KindAdhoc, jobs.StatusRunning, jobs.TriggerManual, nil, []uuid.UUID{w.hostX, w.hostY}},
		{jobs.KindRun, jobs.StatusFailed, "webhook", &tmplQ, []uuid.UUID{w.hostY}},
		{jobs.KindRun, jobs.StatusRunning, jobs.TriggerManual, &tmplQ, []uuid.UUID{w.hostX}},
	}
	for i, s := range specs {
		j := newJob(org, zone, s.kind, s.status)
		j.Trigger, j.TemplateID, j.HostIDs = s.trigger, s.tmpl, s.hosts
		j.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := w.store.CreateJob(w.ctx, j, nil); err != nil {
			t.Fatalf("CreateJob %d: %v", i, err)
		}
		w.ids = append(w.ids, j.ID)
	}

	// Another organization's run targets the same kind of data and must
	// never show up in org's lists.
	oz := seedZone(t, d, other)
	oj := newJob(other, oz, jobs.KindRun, jobs.StatusQueued)
	oj.TemplateID = nil
	oj.HostIDs = []uuid.UUID{seedHost(t, d, other, oz, "o")}
	oj.CreatedAt = base.Add(time.Hour)
	if err := w.store.CreateJob(db.WithOrgScope(ctx, other), oj, nil); err != nil {
		t.Fatal(err)
	}
	w.otherOrgJob = oj.ID
	return w
}

// idx maps result jobs back to their seed indexes.
func (w *listPageWorld) idx(t *testing.T, got []jobs.Job) []int {
	t.Helper()
	out := make([]int, len(got))
	for i, j := range got {
		k := slices.Index(w.ids, j.ID)
		if k < 0 {
			t.Fatalf("job %s is not one of org's seeded runs (another organization's run leaked?)", j.ID)
		}
		out[i] = k
	}
	return out
}

func TestSQLStoreListJobsPage(t *testing.T) {
	w := newListPageWorld(t, "jobs-page-store")
	tmplQFilterStatus := jobs.StatusFailed
	succeeded := jobs.StatusSucceeded
	manual := jobs.TriggerManual
	pb := &w.playbookP
	hx, hy := &w.hostX, &w.hostY

	page := func(number, size int, sort string, desc bool) paging.Page {
		return paging.Page{Number: number, Size: size, Sort: sort, Desc: desc}
	}
	created := func(n, size int) paging.Page { return page(n, size, jobs.SortCreated, true) }

	for _, tc := range []struct {
		name      string
		f         jobs.Filter
		p         paging.Page
		want      []int
		wantTotal int
	}{
		{"default newest first", jobs.Filter{}, created(1, 10), []int{5, 4, 3, 2, 1, 0}, 6},
		{"default first page of two", jobs.Filter{}, created(1, 2), []int{5, 4}, 6},
		{"default middle page", jobs.Filter{}, created(2, 2), []int{3, 2}, 6},
		{"default last page", jobs.Filter{}, created(3, 2), []int{1, 0}, 6},
		{"default past the end", jobs.Filter{}, created(4, 2), []int{}, 6},
		{"created ascending", jobs.Filter{}, page(1, 10, jobs.SortCreated, false), []int{0, 1, 2, 3, 4, 5}, 6},
		{"created ascending offset", jobs.Filter{}, page(2, 4, jobs.SortCreated, false), []int{4, 5}, 6},
		{"status ascending", jobs.Filter{}, page(1, 10, jobs.SortStatus, false), []int{2, 5, 3, 0, 4, 1}, 6},
		{"status descending", jobs.Filter{}, page(1, 10, jobs.SortStatus, true), []int{4, 1, 0, 5, 3, 2}, 6},
		{"kind ascending", jobs.Filter{}, page(1, 10, jobs.SortKind, false), []int{3, 1, 5, 4, 2, 0}, 6},
		{"kind descending page", jobs.Filter{}, page(1, 2, jobs.SortKind, true), []int{5, 4}, 6},
		{"trigger ascending", jobs.Filter{}, page(1, 10, jobs.SortTrigger, false), []int{5, 3, 0, 2, 1, 4}, 6},
		{"trigger descending", jobs.Filter{}, page(1, 10, jobs.SortTrigger, true), []int{4, 1, 2, 5, 3, 0}, 6},
		{"status filter", jobs.Filter{Status: &tmplQFilterStatus}, created(1, 10), []int{4, 1}, 2},
		{"trigger filter sorted", jobs.Filter{Trigger: &manual}, page(1, 10, jobs.SortStatus, false), []int{5, 3, 0}, 3},
		{"playbook filter excludes ad-hoc runs", jobs.Filter{PlaybookID: pb}, created(1, 10), []int{2, 0}, 2},
		{"playbook filter sorted", jobs.Filter{PlaybookID: pb}, page(1, 10, jobs.SortStatus, true), []int{0, 2}, 2},
		{"playbook and status", jobs.Filter{PlaybookID: pb, Status: &succeeded}, created(1, 10), []int{0}, 1},

		{"host newest first", jobs.Filter{HostID: hx}, created(1, 10), []int{5, 3, 1, 0}, 4},
		{"host page", jobs.Filter{HostID: hx}, created(2, 3), []int{0}, 4},
		{"host other", jobs.Filter{HostID: hy}, created(1, 10), []int{4, 3, 2}, 3},
		{"host sorted", jobs.Filter{HostID: hx}, page(1, 10, jobs.SortStatus, false), []int{5, 3, 0, 1}, 4},
		{"host sorted page", jobs.Filter{HostID: hy}, page(2, 2, jobs.SortTrigger, false), []int{4}, 3},
		{"host and status", jobs.Filter{HostID: hx, Status: &tmplQFilterStatus}, created(1, 10), []int{1}, 1},
		{"host and playbook", jobs.Filter{HostID: hx, PlaybookID: pb}, created(1, 10), []int{0}, 1},
		{"host and playbook sorted", jobs.Filter{HostID: hy, PlaybookID: pb}, page(1, 10, jobs.SortKind, false), []int{2}, 1},

		{"active", jobs.Filter{Active: true}, created(1, 10), []int{5, 3, 2}, 3},
		{"active page", jobs.Filter{Active: true}, created(2, 2), []int{2}, 3},
		{"active sorted", jobs.Filter{Active: true}, page(1, 10, jobs.SortStatus, false), []int{2, 5, 3}, 3},
		{"active and host", jobs.Filter{Active: true, HostID: hx}, created(1, 10), []int{5, 3}, 2},
		{"active and host other", jobs.Filter{Active: true, HostID: hy}, created(1, 10), []int{3, 2}, 2},
		{"active and playbook", jobs.Filter{Active: true, PlaybookID: pb}, created(1, 10), []int{2}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := w.store.ListJobsPage(w.ctx, w.org, tc.f, tc.p)
			if err != nil {
				t.Fatal(err)
			}
			if gotIdx := w.idx(t, got); !slices.Equal(gotIdx, tc.want) {
				t.Fatalf("runs = %v, want %v", gotIdx, tc.want)
			}
			if total != tc.wantTotal {
				t.Fatalf("total = %d, want %d", total, tc.wantTotal)
			}
		})
	}

	// Another organization's runs are invisible in every branch, also when
	// the caller names that organization's host.
	t.Run("other organization", func(t *testing.T) {
		for _, f := range []jobs.Filter{{}, {Active: true}, {HostID: hx}} {
			for _, p := range []paging.Page{created(1, 100), page(1, 100, jobs.SortStatus, false)} {
				got, total, err := w.store.ListJobsPage(w.ctx, w.org, f, p)
				if err != nil {
					t.Fatal(err)
				}
				for _, j := range got {
					if j.ID == w.otherOrgJob {
						t.Fatalf("filter %+v page %+v leaked another organization's run", f, p)
					}
				}
				if total > 6 {
					t.Fatalf("filter %+v total = %d counts another organization's run", f, total)
				}
			}
		}
	})

	// Service.ListPage passes the page straight to the store.
	t.Run("service pass-through", func(t *testing.T) {
		svc := jobs.NewService(w.store, nil, nil, nil, nil, nil, nil, time.Now, nil)
		got, total, err := svc.ListPage(w.ctx, w.org, jobs.Filter{}, created(1, 2))
		if err != nil || total != 6 || !slices.Equal(w.idx(t, got), []int{5, 4}) {
			t.Fatalf("ListPage = %v, %d, %v", w.idx(t, got), total, err)
		}
	})
}

// TestFollowUpsErrorNamesTheProblem: a rejected follow-up override is a
// FollowUpsError (and an ErrInvalidInput) whose message carries the reason.
func TestFollowUpsErrorNamesTheProblem(t *testing.T) {
	f := newChainFixture(t)
	self := []uuid.UUID{f.tpl}
	_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, FollowUps: &self})
	var fe *jobs.FollowUpsError
	if !errors.As(err, &fe) || !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("err = %v, want a FollowUpsError wrapping ErrInvalidInput", err)
	}
	if !strings.Contains(fe.Error(), "cannot follow itself") {
		t.Fatalf("message = %q", fe.Error())
	}
}

type allowAllGuard struct{}

func (allowAllGuard) CheckRunClaim(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// TestSetClaimGuardAcceptsAGuard: wiring a guard into a fresh queue is
// accepted and the queue still answers an empty claim with nothing.
func TestSetClaimGuardAcceptsAGuard(t *testing.T) {
	d := testdb.Open(t)
	q := newTestQueue(d)
	q.SetClaimGuard(allowAllGuard{})
	claim, err := q.Claim(ctx, uuid.Must(uuid.NewV7()), nil, "test")
	if err != nil || claim != nil {
		t.Fatalf("claim on an empty queue = %v, %v; want nothing", claim, err)
	}
}

// TestSQLStoreListJobsFailuresAreWrapped: a malformed cursor and a database
// that cannot answer (a cancelled context) surface as errors on every
// branch of both list paths instead of an empty page.
func TestSQLStoreListJobsFailuresAreWrapped(t *testing.T) {
	w := newListPageWorld(t, "jobs-page-failures")
	if _, _, err := w.store.ListJobs(w.ctx, w.org, jobs.Filter{}, "not-a-cursor", 10); err == nil {
		t.Fatal("a malformed cursor was accepted")
	}

	dead, cancel := context.WithCancel(w.ctx)
	cancel()
	filters := map[string]jobs.Filter{
		"all":    {},
		"host":   {HostID: &w.hostX},
		"active": {Active: true},
	}
	for name, f := range filters {
		if _, _, err := w.store.ListJobs(dead, w.org, f, "", 10); err == nil {
			t.Errorf("ListJobs %s on a cancelled context returned no error", name)
		}
		for _, p := range []paging.Page{
			{Number: 1, Size: 10, Sort: jobs.SortCreated, Desc: true},
			{Number: 1, Size: 10, Sort: jobs.SortKind},
		} {
			if _, _, err := w.store.ListJobsPage(dead, w.org, f, p); err == nil {
				t.Errorf("ListJobsPage %s %+v on a cancelled context returned no error", name, p)
			}
		}
	}
}
