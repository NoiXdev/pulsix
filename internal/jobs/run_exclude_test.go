package jobs_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

func TestRunTargetsListsEveryZonesHosts(t *testing.T) {
	svc, _, org, templateID := runFixture(t)
	got, err := svc.RunTargets(ctx, org, templateID)
	if err != nil || len(got) != 2 {
		t.Fatalf("RunTargets = %v, %v; want both zones' hosts", got, err)
	}
	if _, err := svc.RunTargets(ctx, org, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("an unknown template resolved")
	}
}

func TestRunTargetsPropagatesScopeAndResolverErrors(t *testing.T) {
	store := newFakeStore()
	org, bad, good := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		bad:  {ID: bad, OrgID: org, Scope: json.RawMessage(`{`)},
		good: {ID: good, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	res := &fakeResolver{err: errors.New("inventory down")}
	svc := newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{})
	if _, err := svc.RunTargets(ctx, org, bad); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("broken scope = %v", err)
	}
	if _, err := svc.RunTargets(ctx, org, good); err == nil {
		t.Fatal("a resolver failure must surface")
	}
}

func TestEnqueueRunWithExcludesHosts(t *testing.T) {
	svc, _, org, templateID := runFixture(t)
	hosts, err := svc.RunTargets(ctx, org, templateID)
	if err != nil {
		t.Fatal(err)
	}
	ref := uuid.Must(uuid.NewV7())
	created, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{
		Trigger: jobs.TriggerSchedule, TriggerRef: &ref, ExcludeHosts: []uuid.UUID{hosts[0]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || len(created[0].HostIDs) != 1 || created[0].HostIDs[0] != hosts[1] {
		t.Fatalf("jobs = %+v, want one job for the remaining host", created)
	}
	// Backlog 2.81: the job also records how many the request dropped.
	// Without it the run's own numbers describe only what remained, and
	// the finished-run message says "1 host (ok 1)" for a two-host
	// scope with nothing about the one left out for a window.
	if created[0].HostsExcluded != 1 {
		t.Fatalf("hosts excluded = %d, want 1", created[0].HostsExcluded)
	}
	if _, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{
		Trigger: jobs.TriggerSchedule, TriggerRef: &ref, ExcludeHosts: hosts,
	}); !errors.Is(err, jobs.ErrAllHostsExcluded) {
		t.Fatalf("every host excluded = %v, want ErrAllHostsExcluded", err)
	}
}

func TestGroupSummariesDelegatesAndSkipsEmptyInput(t *testing.T) {
	svc, store, org, _ := runFixture(t)
	group := uuid.Must(uuid.NewV7())
	store.summaries = map[uuid.UUID]jobs.GroupSummary{group: {GroupID: group, Status: jobs.StatusFailed}}
	got, err := svc.GroupSummaries(ctx, org, []uuid.UUID{group, uuid.Must(uuid.NewV7())})
	if err != nil || len(got) != 1 || got[group].Status != jobs.StatusFailed {
		t.Fatalf("GroupSummaries = %v, %v", got, err)
	}
	if got, err := svc.GroupSummaries(ctx, org, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
}

func TestGroupSummaryDurationAndHostsOK(t *testing.T) {
	start := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Minute)
	if d := (jobs.GroupSummary{Status: jobs.StatusSucceeded, StartedAt: &start, FinishedAt: &end}).Duration(); d != 4*time.Minute {
		t.Fatalf("finished duration = %s", d)
	}
	if d := (jobs.GroupSummary{Status: jobs.StatusRunning, StartedAt: &start, FinishedAt: &end}).Duration(); d != 0 {
		t.Fatalf("running duration = %s, want 0", d)
	}
	if n := (jobs.RunGroupResult{Hosts: 1, HostsFailed: 3}).HostsOK(); n != 0 {
		t.Fatalf("HostsOK = %d, want 0 when a host counts in several failure lists", n)
	}
}

// TestEnqueueRunWithoutAnExclusionRecordsNone: the count is only about
// the maintenance exclusion, so an ordinary run carries a zero and its
// message stays as quiet as before (backlog 2.81).
func TestEnqueueRunWithoutAnExclusionRecordsNone(t *testing.T) {
	svc, _, org, templateID := runFixture(t)
	ref := uuid.Must(uuid.NewV7())
	created, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range created {
		if j.HostsExcluded != 0 {
			t.Fatalf("hosts excluded = %d on a run with no exclusion", j.HostsExcluded)
		}
	}
}
