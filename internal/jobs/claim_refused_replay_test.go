package jobs_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// refusedAtClaim is the error text the queue's claim guard path writes
// (claimRefusedPrefix plus the guard's reason).
const refusedAtClaim = "refused at claim: the run's request is not approved"

// TestEnqueueRunWithReplayRefillsAZoneRefusedAtClaim: an approval's first
// enqueue created zone 1's job and failed before zone 2's, the request went
// back to pending, and the claim guard refused zone 1's job. A job the
// guard refused ran nothing anywhere, so the replay under the same key must
// enqueue zone 1 again (and zone 2) and leave the refused job out of the
// run; a replayed job that got past the guard, or was claimed before the
// claim that refused it, still covers its zone, so nothing runs twice.
func TestEnqueueRunWithReplayRefillsAZoneRefusedAtClaim(t *testing.T) {
	started := time.Now().UTC().Truncate(time.Microsecond)
	cases := []struct {
		name   string
		mark   func(j *jobs.Job)
		refill bool
	}{
		{"refused at claim", func(j *jobs.Job) {
			j.Status, j.Error, j.Attempts, j.StartedAt, j.FinishedAt = jobs.StatusError, refusedAtClaim, 1, &started, &started
		}, true},
		// Claimed before the revert, its worker lost, requeued and refused
		// on the next claim: the first claim may have run the play.
		{"refused on a later claim", func(j *jobs.Job) {
			j.Status, j.Error, j.Attempts, j.StartedAt, j.FinishedAt = jobs.StatusError, refusedAtClaim, 2, &started, &started
		}, false},
		{"still queued", func(*jobs.Job) {}, false},
		{"running", func(j *jobs.Job) { j.Status, j.StartedAt = jobs.StatusRunning, &started }, false},
		{"failed after it started", func(j *jobs.Job) {
			j.Status, j.Error, j.StartedAt, j.FinishedAt = jobs.StatusFailed, "", &started, &started
		}, false},
		{"errored for another reason", func(j *jobs.Job) {
			j.Status, j.Error, j.StartedAt, j.FinishedAt = jobs.StatusError, "build payload: the template's scope is broken", &started, &started
		}, false},
		{"errored naming the refusal later in its text", func(j *jobs.Job) {
			j.Status, j.Error = jobs.StatusError, "worker lost: "+refusedAtClaim
		}, false},
		{"succeeded", func(j *jobs.Job) { j.Status, j.StartedAt, j.FinishedAt = jobs.StatusSucceeded, &started, &started }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
				templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
			}}
			zone1, zone2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			res := &fakeResolver{targets: []jobs.ZoneTargets{
				{ZoneID: zone1, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
				{ZoneID: zone2, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
			}}
			svc := newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{})
			actor, group := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			req := jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &actor, IdempotencyKey: "run-request:" + group.String(), RunGroupID: group}

			store.failCreateForZone = zone2
			if _, err := svc.EnqueueRunWith(ctx, org, templateID, req); err == nil {
				t.Fatal("first attempt: want the simulated zone 2 failure, got nil")
			}
			if len(store.jobs) != 1 {
				t.Fatalf("jobs after the interrupted attempt = %d, want zone 1's only", len(store.jobs))
			}
			var first uuid.UUID
			for id, s := range store.jobs {
				first = id
				tc.mark(&s.j)
				store.jobs[id] = s
			}

			store.failCreateForZone = uuid.Nil
			created, err := svc.EnqueueRunWith(ctx, org, templateID, req)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			perZone := map[uuid.UUID]int{}
			for _, j := range created {
				if j.RunGroupID != group {
					t.Fatalf("job %s joined group %s, want %s", j.ID, j.RunGroupID, group)
				}
				perZone[j.ZoneID]++
			}
			if len(created) != 2 || perZone[zone1] != 1 || perZone[zone2] != 1 {
				t.Fatalf("replay returned %d jobs by zone %v, want one per zone", len(created), perZone)
			}
			reused := false
			for _, j := range created {
				reused = reused || j.ID == first
			}
			if tc.refill {
				if reused || len(store.jobs) != 3 {
					t.Fatalf("refused job counted = %v, stored jobs = %d; want a new zone 1 job beside the refused one (3)", reused, len(store.jobs))
				}
				return
			}
			if !reused || len(store.jobs) != 2 {
				t.Fatalf("replayed job reused = %v, stored jobs = %d; want zone 1 covered by its first job (2)", reused, len(store.jobs))
			}
		})
	}
}

// TestEnqueueRunWithReplayOfOnlyRefusedJobsFailsWhenTheScopeIsGone: every
// job the first attempt created was refused at claim, and the scope no
// longer resolves when the request is approved again. The replay has
// nothing that ran to fall back on, so the scope's error is returned (the
// approval fails and the request stays pending), not the refused jobs as
// if the run had been enqueued.
func TestEnqueueRunWithReplayOfOnlyRefusedJobsFailsWhenTheScopeIsGone(t *testing.T) {
	store := newFakeStore()
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	zone1, zone2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	res := &fakeResolver{targets: []jobs.ZoneTargets{
		{ZoneID: zone1, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
		{ZoneID: zone2, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
	}}
	svc := newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{})
	actor, group := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	req := jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &actor, IdempotencyKey: "run-request:" + group.String(), RunGroupID: group}

	store.failCreateForZone = zone2
	if _, err := svc.EnqueueRunWith(ctx, org, templateID, req); err == nil {
		t.Fatal("first attempt: want the simulated zone 2 failure, got nil")
	}
	for id, s := range store.jobs {
		s.j.Status, s.j.Error, s.j.Attempts = jobs.StatusError, refusedAtClaim, 1
		store.jobs[id] = s
	}

	store.failCreateForZone = uuid.Nil
	res.err = jobs.ErrEmptyScope
	created, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if !errors.Is(err, jobs.ErrEmptyScope) || len(created) != 0 {
		t.Fatalf("replay = %d jobs, %v; want ErrEmptyScope and no jobs", len(created), err)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("stored jobs = %d, want the refused one only", len(store.jobs))
	}
}
