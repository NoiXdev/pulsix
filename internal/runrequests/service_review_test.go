package runrequests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// fakeReviewApply is the review side of fakeAutomation.
type fakeReviewApply struct {
	applied   []runrequests.Request
	appliedBy uuid.UUID
	err       error
}

func (f *fakeAutomation) ApplyReview(_ context.Context, _ uuid.UUID, r runrequests.Request, _ runrequests.PictureReview, by uuid.UUID) error {
	if f.reviews.err != nil {
		return f.reviews.err
	}
	f.reviews.applied, f.reviews.appliedBy = append(f.reviews.applied, r), by
	return nil
}

// insertReview stores a review the way the Reviewer does: nobody asks for
// it through Create.
func (w *wiring) insertReview(t *testing.T, kind runrequests.Kind) runrequests.Request {
	t.Helper()
	target, tok := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now()
	r := runrequests.Request{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Kind: kind, TemplateID: uuid.Must(uuid.NewV7()),
		Reason: "an assistant changed job template deploy", RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok,
		Status: runrequests.StatusPending, CreatedAt: now, ExpiresAt: now.Add(runrequests.ReviewTTL), TargetID: &target,
		Review: &runrequests.ReviewChange{Changes: []assistantchange.Change{{Subject: assistantchange.SubjectTemplate, Name: "deploy"}}}}
	if err := w.store.Insert(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestApproveReview (spec 2026-11-25 C2): approving a schedule review needs
// templates.write; a repository sync trigger's review repos.write; the
// picture must be the one seen and its item must exist; the decision and
// the resume are one transaction.
func TestApproveReview(t *testing.T) {
	ctx := context.Background()
	w := newWiring()
	w.pics.p = runrequests.Picture{TemplateName: "deploy", Review: &runrequests.PictureReview{Item: runrequests.ReviewItemSchedule,
		Schedule: &runrequests.ScheduleState{Name: "nightly", Paused: true, Revision: 3}, PausedForReview: true}}
	r := w.insertReview(t, runrequests.KindScheduleReview)
	_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	approver := uuid.Must(uuid.NewV7())
	if _, err := w.svc.ApproveWith(ctx, w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteRepos: true}); !errors.Is(err, runrequests.ErrNotAllowed) {
		t.Fatalf("without templates.write = %v", err)
	}
	if _, err := w.svc.ApproveWith(ctx, w.org, r.ID, approver, "stale", nil, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrChanged) {
		t.Fatalf("stale fingerprint = %v", err)
	}
	none := []uuid.UUID{}
	if _, err := w.svc.ApproveWith(ctx, w.org, r.ID, approver, fp, &none, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("follow-ups = %v", err)
	}
	w.auto.reviews.err = errors.New("boom")
	if _, err := w.svc.ApproveWith(ctx, w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true}); err == nil {
		t.Fatal("a failing resume approved the review")
	}
	if got, _ := w.store.Get(ctx, w.org, r.ID); got.Status != runrequests.StatusPending {
		t.Fatalf("after a failing resume = %+v", got)
	}
	w.auto.reviews.err = nil
	a, err := w.svc.ApproveWith(ctx, w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.Request.Status != runrequests.StatusApproved || a.Request.RunGroupID != nil || a.Review == nil ||
		len(w.auto.reviews.applied) != 1 || w.auto.reviews.appliedBy != approver {
		t.Fatalf("approved = %+v, applied %+v", a, w.auto.reviews)
	}

	// A repository sync trigger's review needs repos.write; a gone item
	// cannot be approved; a rejection leaves the item as it is.
	w.pics.p = runrequests.Picture{Review: &runrequests.PictureReview{Item: runrequests.ReviewItemTrigger,
		Trigger: &runrequests.TriggerState{Kind: "repo_sync", Name: "push", Revision: 2}, PausedForReview: true}}
	tr := w.insertReview(t, runrequests.KindTriggerReview)
	_, _, fp, _ = w.svc.Get(ctx, w.org, tr.ID)
	if _, err := w.svc.ApproveWith(ctx, w.org, tr.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrNotAllowed) {
		t.Fatalf("repo sync review without repos.write = %v", err)
	}
	w.pics.p.Review.Gone = true
	_, _, fp, _ = w.svc.Get(ctx, w.org, tr.ID)
	if _, err := w.svc.ApproveWith(ctx, w.org, tr.ID, approver, fp, nil, runrequests.Approver{WriteRepos: true}); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("gone = %v", err)
	}
	if rej, err := w.svc.Reject(ctx, w.org, tr.ID, approver); err != nil || rej.Status != runrequests.StatusRejected || len(w.auto.reviews.applied) != 1 {
		t.Fatalf("reject = %+v %v", rej, err)
	}
}

// Nobody asks for a review: Create refuses the kind.
func TestCreateRefusesReviewKinds(t *testing.T) {
	w := newWiring()
	for _, kind := range []runrequests.Kind{runrequests.KindScheduleReview, runrequests.KindTriggerReview} {
		if _, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{Kind: kind, Reason: "x", TemplateID: uuid.Must(uuid.NewV7())}); !errors.Is(err, runrequests.ErrInvalidInput) {
			t.Fatalf("%s = %v", kind, err)
		}
	}
}
