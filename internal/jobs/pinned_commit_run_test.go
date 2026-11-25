package jobs_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

const pin = "0123456789abcdef0123456789abcdef01234567"

// pinFixture is a chain fixture whose template runs a repository
// playbook, plus an editor-playbook template.
func pinFixture(t *testing.T) (chainFixture, uuid.UUID) {
	t.Helper()
	f := newChainFixture(t)
	repo, inline := id7(), id7()
	row := f.tmpl.byID[f.tpl]
	row.PlaybookRepo, row.PlaybookPath = &repo, "site.yml"
	f.tmpl.byID[f.tpl] = row
	f.tmpl.byID[inline] = jobs.TemplateRow{ID: inline, OrgID: f.org, Scope: json.RawMessage(`{}`), PlaybookInline: "- hosts: all"}
	return f, inline
}

// TestEnqueueRunPinsEveryJobToTheCommit (spec 2026-11-25 A, D3): an
// approved run's commit lands on every job it enqueues.
func TestEnqueueRunPinsEveryJobToTheCommit(t *testing.T) {
	f, _ := pinFixture(t)
	f.res.targets = []jobs.ZoneTargets{{ZoneID: f.zone, HostIDs: []uuid.UUID{f.h1}}, {ZoneID: id7(), HostIDs: []uuid.UUID{f.h2}}}
	got, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, PinnedCommit: pin})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("jobs = %d, want one per zone", len(got))
	}
	repo := f.tmpl.byID[f.tpl].PlaybookRepo
	for _, j := range got {
		if f.store.jobs[j.ID].j.PinnedCommit != pin {
			t.Fatalf("job %s stored with pin %q, want %q", j.ID, f.store.jobs[j.ID].j.PinnedCommit, pin)
		}
		// The pin's repository is recorded with it (final review M4).
		if r := f.store.jobs[j.ID].j.PinnedRepoID; r == nil || *r != *repo {
			t.Fatalf("job %s stored with pin repository %v, want %s", j.ID, r, *repo)
		}
	}
	// An unpinned run records no repository.
	plain, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual})
	if err != nil || f.store.jobs[plain[0].ID].j.PinnedRepoID != nil {
		t.Fatalf("unpinned run = %+v, %v", plain, err)
	}
}

// TestRetryOfAPinFromAnotherRepositoryIsRefused (final review M4): a
// retry runs its original job's commit, which belongs to the repository
// the original ran; once the template's playbook comes from another
// repository, or none, the retry is refused with a clear reason instead of
// failing on the worker.
func TestRetryOfAPinFromAnotherRepositoryIsRefused(t *testing.T) {
	f, inline := pinFixture(t)
	repo := *f.tmpl.byID[f.tpl].PlaybookRepo
	for name, tc := range map[string]struct {
		tmpl    uuid.UUID
		pinRepo uuid.UUID
		refused bool
	}{
		"same repository":    {f.tpl, repo, false},
		"another repository": {f.tpl, id7(), true},
		"an editor playbook": {inline, repo, true},
	} {
		t.Run(name, func(t *testing.T) {
			orig := id7()
			tmpl := tc.tmpl
			pinRepo := tc.pinRepo
			f.store.jobs[orig] = storedJob{j: jobs.Job{
				ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
				TemplateID: &tmpl, HostIDs: []uuid.UUID{f.h1}, RunGroupID: id7(), PinnedCommit: pin, PinnedRepoID: &pinRepo,
				Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String()}},
			}}
			_, err := f.svc.RetryFailed(ctx, f.org, orig, nil)
			if tc.refused != (err != nil) || (tc.refused && !errors.Is(err, jobs.ErrInvalidInput)) {
				t.Fatalf("retry = %v, refused want %v", err, tc.refused)
			}
			if tc.refused && name == "another repository" && !errors.Is(err, jobs.ErrPinnedRepoMoved) {
				t.Fatalf("retry = %v, want ErrPinnedRepoMoved", err)
			}
		})
	}
}

// TestEnqueueRunRefusesAPinItCannotHonour: only a manual run of a
// repository playbook can be pinned, and only to a full commit id.
func TestEnqueueRunRefusesAPinItCannotHonour(t *testing.T) {
	f, inline := pinFixture(t)
	ref := id7()
	for name, tc := range map[string]struct {
		tmpl uuid.UUID
		req  jobs.RunRequest
	}{
		"a branch name":      {f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, PinnedCommit: "main"}},
		"a short commit":     {f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, PinnedCommit: pin[:12]}},
		"an upper-case id":   {f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, PinnedCommit: "0123456789ABCDEF0123456789ABCDEF01234567"}},
		"a schedule run":     {f.tpl, jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref, PinnedCommit: pin}},
		"an editor playbook": {inline, jobs.RunRequest{Trigger: jobs.TriggerManual, PinnedCommit: pin}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.EnqueueRunWith(ctx, f.org, tc.tmpl, tc.req); !errors.Is(err, jobs.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
	if len(f.store.jobs) != 0 {
		t.Fatalf("stored jobs = %d, want none", len(f.store.jobs))
	}
}

// TestRetryRunsTheOriginalJobsCommit: a retry of a pinned job runs that
// job's commit whatever it is asked for; a retry of an unpinned job runs
// the commit it is given (none: the branch tip, as before).
func TestRetryRunsTheOriginalJobsCommit(t *testing.T) {
	other := "fedcba9876543210fedcba9876543210fedcba98"
	for name, tc := range map[string]struct {
		original, given, want string
	}{
		"pinned original":           {pin, "", pin},
		"pinned original, asked":    {pin, other, pin},
		"unpinned original, given":  {"", other, other},
		"unpinned original, no pin": {"", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := pinFixture(t)
			orig := id7()
			f.store.jobs[orig] = storedJob{j: jobs.Job{
				ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
				TemplateID: &f.tpl, HostIDs: []uuid.UUID{f.h1}, RunGroupID: id7(), PinnedCommit: tc.original,
				Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String()}},
			}}
			got, err := f.svc.RetryFailedInto(ctx, f.org, orig, nil, "", id7(), nil, jobs.ApprovedRun{PinnedCommit: tc.given}, nil)
			if err != nil || len(got) != 1 {
				t.Fatalf("retry = %+v, %v", got, err)
			}
			if pinned := f.store.jobs[got[0].ID].j.PinnedCommit; pinned != tc.want {
				t.Fatalf("retry pinned to %q, want %q", pinned, tc.want)
			}
			if c, err := f.svc.PinnedCommit(ctx, f.org, orig); err != nil || c.Commit != tc.original {
				t.Fatalf("PinnedCommit(original) = %+v, %v; want %q", c, err, tc.original)
			}
		})
	}
}

// TestFollowUpOfAPinnedRunOnTheSameRepositoryIsPinned (controller ruling,
// fix round 1): a follow-up started by a pinned run runs the same commit
// when its template's playbook comes from the same repository; a follow-up
// of another repository or an editor playbook runs as before, and so does
// every follow-up of an unpinned run.
func TestFollowUpOfAPinnedRunOnTheSameRepositoryIsPinned(t *testing.T) {
	f, inline := pinFixture(t)
	sameRepo, otherRepo := id7(), id7()
	parentRow := f.tmpl.byID[f.tpl]
	f.tmpl.byID[sameRepo] = jobs.TemplateRow{ID: sameRepo, OrgID: f.org, Scope: json.RawMessage(`{}`), PlaybookRepo: parentRow.PlaybookRepo, PlaybookPath: "db.yml"}
	other := id7()
	f.tmpl.byID[otherRepo] = jobs.TemplateRow{ID: otherRepo, OrgID: f.org, Scope: json.RawMessage(`{}`), PlaybookRepo: &other, PlaybookPath: "site.yml"}

	parent := func(t *testing.T, pinned string) uuid.UUID {
		t.Helper()
		got, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, PinnedCommit: pinned})
		if err != nil || len(got) == 0 {
			t.Fatalf("parent = %+v, %v", got, err)
		}
		return got[0].RunGroupID
	}
	chainRun := func(t *testing.T, tmpl, group uuid.UUID) string {
		t.Helper()
		got, err := f.svc.EnqueueRunWith(ctx, f.org, tmpl, jobs.RunRequest{
			Trigger: jobs.TriggerChain, TriggerRef: &group, IdempotencyKey: "chain:" + id7().String(),
			Chain: &jobs.ChainContext{RootTrigger: jobs.TriggerManual, Depth: 1, Path: []uuid.UUID{f.tpl}},
		})
		if err != nil || len(got) == 0 {
			t.Fatalf("follow-up = %+v, %v", got, err)
		}
		return f.store.jobs[got[0].ID].j.PinnedCommit
	}

	pinnedGroup := parent(t, pin)
	if got := chainRun(t, sameRepo, pinnedGroup); got != pin {
		t.Fatalf("same repository follow-up pinned to %q, want %s", got, pin)
	}
	if got := chainRun(t, otherRepo, pinnedGroup); got != "" {
		t.Fatalf("other repository follow-up pinned to %q, want none", got)
	}
	if got := chainRun(t, inline, pinnedGroup); got != "" {
		t.Fatalf("editor playbook follow-up pinned to %q, want none", got)
	}
	if got := chainRun(t, sameRepo, parent(t, "")); got != "" {
		t.Fatalf("follow-up of an unpinned run pinned to %q, want none", got)
	}
}
