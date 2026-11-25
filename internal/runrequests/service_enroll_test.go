package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestApproveOfAnEnrollingRunNeedsTheAgentsPermission (spec 2026-11-25 B,
// "Who may"): a picture with agent enrollment, a dry run's included, is
// approved only by a person who may enrol agents; otherwise nothing is
// decided or enqueued.
func TestApproveOfAnEnrollingRunNeedsTheAgentsPermission(t *testing.T) {
	for name, e := range map[string]runrequests.PictureAgentEnrollment{
		"hosts":   {Hosts: []string{"web-2"}, HostIDs: []string{uuid.Must(uuid.NewV7()).String()}},
		"dry run": {Hosts: []string{}, HostIDs: []string{}, NoneReason: runrequests.EnrollNoneDryRun},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			w := newWiring()
			w.pics.p.AgentEnrollment = &e
			r := w.create(t)
			_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
			if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, false); !errors.Is(err, runrequests.ErrEnrollmentNotAllowed) {
				t.Fatalf("Approve = %v, want ErrEnrollmentNotAllowed", err)
			}
			if got := w.store.rows[r.ID]; got.Status != runrequests.StatusPending || len(w.enq.runs) != 0 {
				t.Fatalf("after the refusal: %s, %d runs", got.Status, len(w.enq.runs))
			}
			if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, true); err != nil {
				t.Fatalf("Approve with the permission = %v", err)
			}
		})
	}
}

// TestApproveStoresTheEnrollList: the approval stores the picture's hosts
// on the request and hands them to the enqueue, a run and a retry alike;
// a dry run stores an empty list, a picture without the section none.
func TestApproveStoresTheEnrollList(t *testing.T) {
	ctx := context.Background()
	h1, h2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	approve := func(t *testing.T, w *wiring, r runrequests.Request) {
		t.Helper()
		_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
		if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("run", func(t *testing.T) {
		w := newWiring()
		w.pics.p.AgentEnrollment = &runrequests.PictureAgentEnrollment{Hosts: []string{"a", "b"}, HostIDs: []string{h1.String(), h2.String()}}
		r := w.create(t)
		approve(t, w, r)
		want := []uuid.UUID{h1, h2}
		if !slices.Equal(w.store.rows[r.ID].EnrollHostIDs, want) || !slices.Equal(w.enq.request.EnrollHostIDs, want) {
			t.Fatalf("stored %v, enqueued %v; want %v", w.store.rows[r.ID].EnrollHostIDs, w.enq.request.EnrollHostIDs, want)
		}
	})
	t.Run("retry", func(t *testing.T) {
		w := newWiring()
		w.pics.p.AgentEnrollment = &runrequests.PictureAgentEnrollment{Hosts: []string{"a"}, HostIDs: []string{h1.String()}}
		job := uuid.Must(uuid.NewV7())
		r, err := w.svc.Create(ctx, w.org, runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job, Reason: "retry", RequestedByUser: uuid.Must(uuid.NewV7())})
		if err != nil {
			t.Fatal(err)
		}
		approve(t, w, r)
		if !slices.Equal(w.enq.retryApproved.EnrollHostIDs, []uuid.UUID{h1}) || !slices.Equal(w.store.rows[r.ID].EnrollHostIDs, []uuid.UUID{h1}) {
			t.Fatalf("retry enqueued %+v, stored %v; want [%s]", w.enq.retryApproved, w.store.rows[r.ID].EnrollHostIDs, h1)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		w := newWiring()
		w.pics.p.AgentEnrollment = &runrequests.PictureAgentEnrollment{Hosts: []string{}, HostIDs: []string{}, NoneReason: runrequests.EnrollNoneDryRun}
		r := w.create(t)
		approve(t, w, r)
		if got := w.store.rows[r.ID].EnrollHostIDs; got == nil || len(got) != 0 {
			t.Fatalf("dry run stored %#v, want an empty list", got)
		}
	})
	t.Run("no enrollment", func(t *testing.T) {
		w := newWiring()
		r := w.create(t)
		approve(t, w, r)
		if w.store.rows[r.ID].EnrollHostIDs != nil || w.enq.request.EnrollHostIDs != nil {
			t.Fatalf("stored %v, enqueued %v; want none", w.store.rows[r.ID].EnrollHostIDs, w.enq.request.EnrollHostIDs)
		}
	})
}

// TestCheckRunClaimComparesTheStoredEnrollList: a job of an approved run
// is claimed only while it carries the list the approval stored, in any
// order; no list and an empty one differ.
func TestCheckRunClaimComparesTheStoredEnrollList(t *testing.T) {
	ctx := context.Background()
	h1, h2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	w := newWiring()
	w.pics.p.AgentEnrollment = &runrequests.PictureAgentEnrollment{Hosts: []string{"a", "b"}, HostIDs: []string{h1.String(), h2.String()}}
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID, jobs.ApprovedRun{EnrollHostIDs: []uuid.UUID{h2, h1}}); err != nil {
		t.Fatalf("the approved list's claim = %v", err)
	}
	for name, list := range map[string][]uuid.UUID{
		"no list":    nil,
		"empty list": {},
		"a host off": {h1},
		"another":    {h1, uuid.Must(uuid.NewV7())},
	} {
		err := w.svc.CheckRunClaim(ctx, w.org, r.ID, jobs.ApprovedRun{EnrollHostIDs: list})
		if !errors.Is(err, runrequests.ErrChangedAfterApproval) {
			t.Errorf("%s = %v, want ErrChangedAfterApproval", name, err)
		}
	}
}
