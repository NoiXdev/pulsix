package jobs_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// checkRun is the request a playbook page's dry run makes (spec
// 2026-11-22 §1): a manual run in check mode.
func checkRun(ov jobs.RunOverrides) jobs.RunRequest {
	ov.Check = true
	return jobs.RunRequest{Trigger: jobs.TriggerManual, Overrides: &ov}
}

// openOverrides unseals what a job's payload carries (fakeSealer).
func openOverrides(t *testing.T, f chainFixture, j jobs.Job) jobs.RunOverrides {
	t.Helper()
	var b64 string
	if err := json.Unmarshal(f.store.jobs[j.ID].payload, &b64); err != nil {
		t.Fatalf("payload of %s is not a JSON string: %v", j.ID, err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fakeSealer{}.OpenAAD(raw, jobs.JobAAD(f.org, j.ID))
	if err != nil {
		t.Fatal(err)
	}
	var ov jobs.RunOverrides
	if err := json.Unmarshal(plain, &ov); err != nil {
		t.Fatal(err)
	}
	return ov
}

// TestDraftNeedsCheckMode is Review Focus 2: a draft (Playbook override)
// never runs for real, whoever asks — the service refuses it without
// Check, and nothing is enqueued.
func TestDraftNeedsCheckMode(t *testing.T) {
	f := newChainFixture(t)
	draft := "- hosts: all\n  tasks: []\n"
	for _, req := range []jobs.RunRequest{
		{Trigger: jobs.TriggerManual, Overrides: &jobs.RunOverrides{Playbook: &draft}},
		{Trigger: jobs.TriggerManual, Overrides: &jobs.RunOverrides{Playbook: &draft, Diff: true, Tags: []string{"web"}}},
	} {
		_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req)
		if !errors.Is(err, jobs.ErrInvalidDryRun) {
			t.Fatalf("err = %v, want ErrInvalidDryRun", err)
		}
	}
	if len(f.store.jobs) != 0 || len(f.planner.plans) != 0 {
		t.Fatalf("stored %d jobs and %d plans, want none", len(f.store.jobs), len(f.planner.plans))
	}
	empty := ""
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, checkRun(jobs.RunOverrides{Playbook: &empty})); !errors.Is(err, jobs.ErrInvalidDryRun) {
		t.Fatalf("empty draft: err = %v, want ErrInvalidDryRun", err)
	}
}

func TestCheckRunValidatesTags(t *testing.T) {
	many := make([]string, 33)
	for i := range many {
		many[i] = "t" + strings.Repeat("x", i%5)
	}
	cases := []struct {
		name string
		tags []string
		ok   bool
	}{
		{"none", nil, true},
		{"typical", []string{"web", "db_2", "role:nginx", "v1.2-rc"}, true},
		{"32 tags of 64", slices.Repeat([]string{strings.Repeat("a", 64)}, 32), true},
		{"33 tags", many, false},
		{"65 characters", []string{strings.Repeat("a", 65)}, false},
		{"comma", []string{"a,b"}, false},
		{"space", []string{"a b"}, false},
		{"empty", []string{""}, false},
		{"newline", []string{"a\n"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newChainFixture(t)
			_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, checkRun(jobs.RunOverrides{Tags: c.tags}))
			if c.ok && err != nil {
				t.Fatalf("err = %v", err)
			}
			if !c.ok && !errors.Is(err, jobs.ErrInvalidDryRun) {
				t.Fatalf("err = %v, want ErrInvalidDryRun", err)
			}
		})
	}
}

// TestCheckRunNarrowsToTheChosenHosts is Review Focus 3: the chosen hosts
// narrow the template's targets and never widen them.
func TestCheckRunNarrowsToTheChosenHosts(t *testing.T) {
	f := newChainFixture(t)
	req := checkRun(jobs.RunOverrides{})
	req.OnlyHosts = []uuid.UUID{f.h2, id7()}
	out, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !slices.Equal(out[0].HostIDs, []uuid.UUID{f.h2}) {
		t.Fatalf("jobs = %+v, want one job on h2", out)
	}
	req.OnlyHosts = []uuid.UUID{id7()}
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req); !errors.Is(err, jobs.ErrHostNotInScope) {
		t.Fatalf("foreign host only: err = %v, want ErrHostNotInScope", err)
	}
}

func TestCheckRunMarksItsJobsAndSealsItsOverrides(t *testing.T) {
	f := newChainFixture(t)
	draft := "- hosts: all\n  tasks: []\n"
	out, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, checkRun(jobs.RunOverrides{
		Diff: true, Tags: []string{"web", "db"}, Playbook: &draft,
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range out {
		if !j.CheckMode || !f.store.jobs[j.ID].j.CheckMode {
			t.Fatalf("job %s: check mode not set (returned %v, stored %v)", j.ID, j.CheckMode, f.store.jobs[j.ID].j.CheckMode)
		}
		ov := openOverrides(t, f, j)
		if !ov.Check || !ov.Diff || !slices.Equal(ov.Tags, []string{"web", "db"}) || ov.Playbook == nil || *ov.Playbook != draft {
			t.Fatalf("sealed overrides = %+v", ov)
		}
	}

	// Check mode alone is an override too: it is sealed, diff stays off.
	out, err = f.svc.EnqueueRunWith(ctx, f.org, f.tpl, checkRun(jobs.RunOverrides{}))
	if err != nil {
		t.Fatal(err)
	}
	if ov := openOverrides(t, f, out[0]); !ov.Check || ov.Diff || ov.Tags != nil || ov.Playbook != nil {
		t.Fatalf("sealed overrides = %+v", ov)
	}

	// A real run is not marked.
	out, err = f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].CheckMode || f.store.jobs[out[0].ID].j.CheckMode {
		t.Fatal("a real run is marked as a check run")
	}
}

// TestCheckRunHasNoFollowUpsOrChain: a dry run plans no follow-ups, even
// when the template has some (the planner would write them), refuses a
// follow-up override, and only a manual run can be one.
func TestCheckRunHasNoFollowUpsOrChain(t *testing.T) {
	f := newChainFixture(t)
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, checkRun(jobs.RunOverrides{})); err != nil {
		t.Fatal(err)
	}
	if len(f.planner.plans) != 0 {
		t.Fatalf("plans = %+v, want none for a check run", f.planner.plans)
	}

	other := f.addTemplate()
	req := checkRun(jobs.RunOverrides{})
	req.FollowUps = &[]uuid.UUID{other}
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req); !errors.Is(err, jobs.ErrInvalidDryRun) {
		t.Fatalf("follow-up override: err = %v, want ErrInvalidDryRun", err)
	}

	ref := id7()
	for _, r := range []jobs.RunRequest{
		{Trigger: jobs.TriggerSchedule, TriggerRef: &ref},
		{Trigger: jobs.TriggerWebhook, TriggerRef: &ref},
		{Trigger: jobs.TriggerSystem},
		{Trigger: jobs.TriggerChain, TriggerRef: &ref, Chain: &jobs.ChainContext{RootTrigger: jobs.TriggerManual, Depth: 1}},
	} {
		r.Overrides = &jobs.RunOverrides{Check: true}
		if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, r); !errors.Is(err, jobs.ErrInvalidDryRun) {
			t.Fatalf("trigger %s: err = %v, want ErrInvalidDryRun", r.Trigger, err)
		}
	}
	if len(f.store.jobs) != 1 || len(f.planner.plans) != 0 {
		t.Fatalf("stored %d jobs and %d plans, want the first check run only", len(f.store.jobs), len(f.planner.plans))
	}
}

// TestCheckRunMustUseThePlaybook: a dry run from a playbook's page names
// the playbook, and the template must run that one; a draft replaces an
// editor playbook only.
func TestCheckRunMustUseThePlaybook(t *testing.T) {
	f := newChainFixture(t)
	pb, repo := id7(), id7()
	row := f.tmpl.byID[f.tpl]
	row.PlaybookID = pb
	f.tmpl.byID[f.tpl] = row

	req := checkRun(jobs.RunOverrides{})
	other := id7()
	req.PlaybookID = &other
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req); !errors.Is(err, jobs.ErrInvalidDryRun) {
		t.Fatalf("another playbook: err = %v, want ErrInvalidDryRun", err)
	}
	req.PlaybookID = &pb
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req); err != nil {
		t.Fatalf("its own playbook: %v", err)
	}

	draft := "- hosts: all\n"
	row.PlaybookRepo = &repo
	f.tmpl.byID[f.tpl] = row
	req = checkRun(jobs.RunOverrides{Playbook: &draft})
	req.PlaybookID = &pb
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req); !errors.Is(err, jobs.ErrInvalidDryRun) {
		t.Fatalf("draft of a repository playbook: err = %v, want ErrInvalidDryRun", err)
	}
	if len(f.store.jobs) != 1 {
		t.Fatalf("stored %d jobs, want the one accepted run", len(f.store.jobs))
	}
}

// TestRetryFailedRefusesACheckRun: retrying a dry run would run its
// failed hosts for real; both the retry and a run request's check refuse.
func TestRetryFailedRefusesACheckRun(t *testing.T) {
	f := newChainFixture(t)
	orig := id7()
	f.store.jobs[orig] = storedJob{j: jobs.Job{
		ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed, CheckMode: true,
		TemplateID: &f.tpl, HostIDs: []uuid.UUID{f.h1}, RunGroupID: id7(),
		Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String()}},
	}}
	if _, err := f.svc.RetryFailed(ctx, f.org, orig, nil); !errors.Is(err, jobs.ErrDryRunNotRetryable) {
		t.Fatalf("RetryFailed: err = %v, want ErrDryRunNotRetryable", err)
	}
	if _, _, _, err := f.svc.RetryTargets(ctx, f.org, orig); !errors.Is(err, jobs.ErrDryRunNotRetryable) {
		t.Fatalf("RetryTargets: err = %v, want ErrDryRunNotRetryable", err)
	}
	if len(f.store.jobs) != 1 || len(f.planner.copies) != 0 {
		t.Fatalf("stored %d jobs and %d copies, want only the original", len(f.store.jobs), len(f.planner.copies))
	}
}

// TestEnqueueSyncRefusesCheckOverrides: the new fields count as overrides.
func TestEnqueueSyncRefusesCheckOverrides(t *testing.T) {
	f := newChainFixture(t)
	if _, err := f.svc.EnqueueSyncWith(ctx, f.org, id7(), jobs.RunRequest{Trigger: jobs.TriggerManual, Overrides: &jobs.RunOverrides{Check: true}}); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestCheckRunRecordsItsOptions: a dry run's job records its non-secret
// options (tags, diff, whether a draft ran) for the run view; the draft's
// text stays in the sealed payload only. An ordinary run records none.
func TestCheckRunRecordsItsOptions(t *testing.T) {
	f := newChainFixture(t)
	draft := "- hosts: all\n  tasks: []\n"
	for _, c := range []struct {
		name string
		req  jobs.RunRequest
		want *jobs.DryRun
	}{
		{"draft with tags", checkRun(jobs.RunOverrides{Diff: true, Tags: []string{"web", "db"}, Playbook: &draft}), &jobs.DryRun{Tags: []string{"web", "db"}, Diff: true, Draft: true}},
		{"saved playbook", checkRun(jobs.RunOverrides{}), &jobs.DryRun{Tags: []string{}}},
		{"real run", jobs.RunRequest{Trigger: jobs.TriggerManual}, nil},
		{"real run with overrides", jobs.RunRequest{Trigger: jobs.TriggerManual, Overrides: &jobs.RunOverrides{ExtraVars: map[string]any{"a": 1}}}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, c.req)
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range out {
				for _, got := range []*jobs.DryRun{j.DryRun, f.store.jobs[j.ID].j.DryRun} {
					if !dryRunEqual(got, c.want) {
						t.Fatalf("dry run = %+v, want %+v", got, c.want)
					}
				}
			}
		})
	}
}

func dryRunEqual(a, b *jobs.DryRun) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Diff == b.Diff && a.Draft == b.Draft && slices.Equal(a.Tags, b.Tags) && (a.Tags == nil) == (b.Tags == nil)
}
