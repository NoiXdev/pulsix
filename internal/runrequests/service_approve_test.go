package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestApproveReportsTheApprovedRevision: the approval names the template
// revision of the picture it approved, not the one the request was made
// against.
func TestApproveReportsTheApprovedRevision(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	w.pics.p.TemplateRevision = 9
	_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.TemplateRevision != 7 || a.TemplateRevision != 9 {
		t.Fatalf("requested at %d, approved revision %d, want 9", r.TemplateRevision, a.TemplateRevision)
	}
}

func TestApproveEnqueuesAsTheApprover(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	approver := uuid.Must(uuid.NewV7())
	approved, err := w.svc.Approve(context.Background(), w.org, r.ID, approver, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := approved.Request
	if got.Status != runrequests.StatusApproved || got.RunGroupID == nil || *got.RunGroupID != w.enq.group || *got.DecidedBy != approver {
		t.Fatalf("approved = %+v", got)
	}
	if len(w.enq.runs) != 1 || w.enq.runs[0] != "run-request:"+r.ID.String() || w.enq.actor != approver {
		t.Fatalf("enqueue = %+v", w.enq)
	}
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, approver, fp, nil); !errors.Is(err, runrequests.ErrDecided) {
		t.Fatalf("second approve = %v, want ErrDecided", err)
	}
}

func TestApproveRefusesAChangedPicture(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	w.pics.p.Limit = "db"
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); !errors.Is(err, runrequests.ErrChanged) {
		t.Fatalf("approve after a change = %v, want ErrChanged", err)
	}
	if len(w.enq.runs) != 0 {
		t.Fatal("a changed request was enqueued")
	}
	got, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if got.Status != runrequests.StatusPending {
		t.Fatalf("status after the refusal = %s, want pending", got.Status)
	}
}

func TestApproveRefusesAnExpiredRequest(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	w.now = w.now.Add(time.Hour)
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); !errors.Is(err, runrequests.ErrExpired) {
		t.Fatalf("approve after expiry = %v, want ErrExpired", err)
	}
}

func TestApproveRevertsWhenTheEnqueueFails(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	w.enq.err = errors.New("no worker zone")
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err == nil {
		t.Fatal("approve with a failing enqueue succeeded")
	}
	got, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if got.Status != runrequests.StatusPending || got.DecidedBy != nil {
		t.Fatalf("after the failed enqueue = %+v, want pending again", got)
	}
}

func TestApproveRetryUsesRetryFailed(t *testing.T) {
	w := newWiring()
	job := uuid.Must(uuid.NewV7())
	r, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job, Reason: "retry", RequestedByUser: uuid.Must(uuid.NewV7())})
	if err != nil {
		t.Fatal(err)
	}
	if r.TemplateID != w.val.template {
		t.Fatalf("retry request template = %s, want the job's", r.TemplateID)
	}
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.enq.retries) != 1 || w.enq.retries[0] != job {
		t.Fatalf("retries = %+v", w.enq.retries)
	}
}

// TestCheckRunClaim: the claim of an approved run passes only while the
// run picture is the one the person approved; a run group of no request
// passes; a request put back to pending refuses its jobs.
func TestCheckRunClaim(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	if err := w.svc.CheckRunClaim(ctx, w.org, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("a run group of no request = %v, want nil", err)
	}
	r := w.create(t)
	_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	if w.enq.group != r.ID {
		t.Fatalf("enqueued into run group %s, want the request's id %s", w.enq.group, r.ID)
	}
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); err != nil {
		t.Fatalf("an unchanged approved run = %v", err)
	}
	w.pics.p.Limit = "web-*"
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); !errors.Is(err, runrequests.ErrChangedAfterApproval) {
		t.Fatalf("a changed approved run = %v, want ErrChangedAfterApproval", err)
	}
	if err := w.store.RevertToPending(ctx, w.org, r.ID); err != nil {
		t.Fatal(err)
	}
	w.pics.p.Limit = "web"
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); err == nil {
		t.Fatal("a reverted request's job passed the claim check")
	}

	// Approved in mode A: a follow-up's revision moving, or the setting
	// flipping, between the approval and the claim fails the run.
	followUp := uuid.Must(uuid.NewV7())
	modeA := func() *runrequests.PictureFollowUps {
		return &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
			{TemplateID: followUp.String(), TemplateName: "listmonk", TemplateRevision: 3},
		}}
	}
	for name, change := range map[string]func(f *runrequests.PictureFollowUps){
		"follow-up revision": func(f *runrequests.PictureFollowUps) { f.Templates[0].TemplateRevision = 4 },
		"setting flipped": func(f *runrequests.PictureFollowUps) {
			f.Start = false
			f.Templates[0].TemplateRevision = 0
		},
	} {
		w.pics.p.FollowUps = modeA()
		r := w.create(t)
		_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
			t.Fatalf("%s: approve = %v", name, err)
		}
		if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); err != nil {
			t.Fatalf("%s: an unchanged approved run = %v", name, err)
		}
		f := modeA()
		change(f)
		w.pics.p.FollowUps = f
		if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); !errors.Is(err, runrequests.ErrChangedAfterApproval) {
			t.Fatalf("%s: claim = %v, want ErrChangedAfterApproval", name, err)
		}
	}
}

// TestApproveSelection: the follow-ups an approval starts come from the
// picture it recomputed and the person's selection, validated before the
// decision.
func TestApproveSelection(t *testing.T) {
	y, z, stopped, foreign := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	modeA := &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
		{TemplateID: y.String(), TemplateName: "y", TemplateRevision: 1},
		{TemplateID: z.String(), TemplateName: "z", TemplateRevision: 2},
		{TemplateID: stopped.String(), TemplateName: "s", Stop: runrequests.StopInChain},
	}}
	modeB := &runrequests.PictureFollowUps{Templates: []runrequests.PictureFollowUp{
		{TemplateID: y.String(), TemplateName: "y"}, {TemplateID: z.String(), TemplateName: "z"},
	}}
	truncated := &runrequests.PictureFollowUps{Start: true, Truncated: true, Templates: modeA.Templates[:2]}
	sel := func(ids ...uuid.UUID) *[]uuid.UUID { return &ids }
	cases := []struct {
		name      string
		followUps *runrequests.PictureFollowUps
		selection *[]uuid.UUID
		want      []uuid.UUID // nil with wantErr
		wantErr   error
	}{
		{"mode B omitted", modeB, nil, []uuid.UUID{}, nil},
		{"mode B empty", modeB, sel(), []uuid.UUID{}, nil},
		{"mode B selected", modeB, sel(y), nil, runrequests.ErrInvalidInput},
		{"mode A omitted", modeA, nil, []uuid.UUID{y, z}, nil},
		{"mode A empty", modeA, sel(), []uuid.UUID{}, nil},
		{"mode A subset", modeA, sel(z), []uuid.UUID{z}, nil},
		{"mode A picture order", modeA, sel(z, y), []uuid.UUID{y, z}, nil},
		{"mode A foreign", modeA, sel(y, foreign), nil, runrequests.ErrInvalidInput},
		{"mode A stop-marked", modeA, sel(stopped), nil, runrequests.ErrInvalidInput},
		{"truncated omitted", truncated, nil, []uuid.UUID{}, nil},
		{"truncated empty", truncated, sel(), []uuid.UUID{}, nil},
		{"truncated selected", truncated, sel(y), nil, runrequests.ErrInvalidInput},
		{"no follow-ups omitted", nil, nil, []uuid.UUID{}, nil},
		{"no follow-ups selected", nil, sel(y), nil, runrequests.ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWiring()
			w.pics.p.FollowUps = tc.followUps
			r := w.create(t)
			_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, tc.selection)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("approve = %v, want %v", err, tc.wantErr)
				}
				if len(w.enq.runs) != 0 {
					t.Fatal("a refused selection was enqueued")
				}
				if after, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID); after.Status != runrequests.StatusPending {
					t.Fatalf("status after the refusal = %s, want pending", after.Status)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.enq.list == nil || !slices.Equal(w.enq.list, tc.want) {
				t.Fatalf("enqueued follow-ups = %v (nil %t), want %v", w.enq.list, w.enq.list == nil, tc.want)
			}
			started := make([]uuid.UUID, len(got.Started))
			for i, n := range got.Started {
				started[i] = uuid.MustParse(n.TemplateID)
			}
			if !slices.Equal(started, tc.want) {
				t.Fatalf("Started = %v, want %v", started, tc.want)
			}
			if got.FollowUps != tc.followUps {
				t.Fatalf("FollowUps = %+v, want the picture's", got.FollowUps)
			}
		})
	}

	t.Run("a changed fingerprint wins", func(t *testing.T) {
		w := newWiring()
		w.pics.p.FollowUps = modeB
		r := w.create(t)
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		w.pics.p.FollowUps = modeA
		if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, sel(foreign)); !errors.Is(err, runrequests.ErrChanged) {
			t.Fatalf("approve = %v, want ErrChanged", err)
		}
	})
}
