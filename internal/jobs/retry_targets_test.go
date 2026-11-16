package jobs_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// TestRetryTargets: what a retry of a finished run would target, without
// enqueueing anything (spec 2026-11-16 assistant access: a retry request
// is validated like the retry itself).
func TestRetryTargets(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h1, h2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.jobs[id] = storedJob{j: jobs.Job{
		ID: id, OrgID: org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
		TemplateID: &templateID, HostIDs: []uuid.UUID{h1, h2}, RunGroupID: uuid.Must(uuid.NewV7()),
		Stats: map[string]any{jobs.StatsFailedHosts: []any{h2.String()}},
	}}
	q := &fakeQueue{}
	svc := newService(store, q, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
	tpl, hosts, err := svc.RetryTargets(ctx, org, id)
	if err != nil || tpl != templateID || len(hosts) != 1 || hosts[0] != h2 {
		t.Fatalf("RetryTargets = %v %v %v", tpl, hosts, err)
	}
	store.jobs[id] = storedJob{j: jobs.Job{ID: id, OrgID: org, Kind: jobs.KindRun, Status: jobs.StatusSucceeded, TemplateID: &templateID}}
	if _, _, err := svc.RetryTargets(ctx, org, id); !errors.Is(err, jobs.ErrNothingToRetry) {
		t.Fatalf("succeeded run = %v, want ErrNothingToRetry", err)
	}
}
