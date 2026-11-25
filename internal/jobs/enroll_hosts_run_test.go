package jobs_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// TestEnqueueRunStoresTheEnrollList (spec 2026-11-25 B): the hosts an
// approval enrols agents on land on every job of the run, as approved; a
// run without a list stores none.
func TestEnqueueRunStoresTheEnrollList(t *testing.T) {
	f := newChainFixture(t)
	f.res.targets = []jobs.ZoneTargets{{ZoneID: f.zone, HostIDs: []uuid.UUID{f.h1}}, {ZoneID: id7(), HostIDs: []uuid.UUID{f.h2}}}
	list := []uuid.UUID{f.h2, f.h1}
	got, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, EnrollHostIDs: list})
	if err != nil || len(got) != 2 {
		t.Fatalf("enqueue = %+v, %v", got, err)
	}
	for _, j := range got {
		if stored := f.store.jobs[j.ID].j.EnrollHostIDs; !slices.Equal(stored, list) {
			t.Fatalf("job %s stored %v, want %v", j.ID, stored, list)
		}
	}
	empty, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, EnrollHostIDs: []uuid.UUID{}})
	if err != nil || f.store.jobs[empty[0].ID].j.EnrollHostIDs == nil {
		t.Fatalf("an empty list = %+v, %v; want it stored as an empty list", empty, err)
	}
	plain, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual})
	if err != nil || f.store.jobs[plain[0].ID].j.EnrollHostIDs != nil {
		t.Fatalf("a run without a list = %+v, %v; want none stored", plain, err)
	}
}

// TestEnqueueRunRefusesAnEnrollListOfATriggeredRun: only an approval
// stores an enroll list, and an approval starts a manual run.
func TestEnqueueRunRefusesAnEnrollListOfATriggeredRun(t *testing.T) {
	f := newChainFixture(t)
	ref := id7()
	_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref, EnrollHostIDs: []uuid.UUID{f.h1}})
	if !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestRetryStoresTheApprovedEnrollList: an approved retry request's list
// lands on the retry's jobs; a person's retry has none.
func TestRetryStoresTheApprovedEnrollList(t *testing.T) {
	f := newChainFixture(t)
	orig := id7()
	f.store.jobs[orig] = storedJob{j: jobs.Job{
		ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
		TemplateID: &f.tpl, HostIDs: []uuid.UUID{f.h1}, RunGroupID: id7(),
		Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String()}},
	}}
	got, err := f.svc.RetryFailedInto(ctx, f.org, orig, nil, "", id7(), nil, jobs.ApprovedRun{EnrollHostIDs: []uuid.UUID{f.h1}}, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("retry = %+v, %v", got, err)
	}
	if stored := f.store.jobs[got[0].ID].j.EnrollHostIDs; !slices.Equal(stored, []uuid.UUID{f.h1}) {
		t.Fatalf("retry stored %v, want [%s]", stored, f.h1)
	}
	plain, err := f.svc.RetryFailed(ctx, f.org, orig, nil)
	if err != nil || f.store.jobs[plain[0].ID].j.EnrollHostIDs != nil {
		t.Fatalf("a retry without a confirmed list = %+v, %v; want no list", plain, err)
	}
	// A person's retry stores the list they confirmed (controller ruling C).
	confirmed, err := f.svc.RetryFailedWith(ctx, f.org, orig, nil, nil, []uuid.UUID{f.h1})
	if err != nil || !slices.Equal(f.store.jobs[confirmed[0].ID].j.EnrollHostIDs, []uuid.UUID{f.h1}) {
		t.Fatalf("a confirmed retry = %+v, %v; want [%s]", confirmed, err, f.h1)
	}
}
