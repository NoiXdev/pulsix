package runrequests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// fakeAutomation stands in for the resolver's schedule side: what a
// request validates to and what an approval applied.
type fakeAutomation struct {
	err        error
	applyErr   error
	template   uuid.UUID
	change     runrequests.ScheduleChange
	validated  []uuid.UUID // the target ids ValidateSchedule was given
	applied    []runrequests.Request
	appliedBy  uuid.UUID
	appliedPic runrequests.PictureSchedule
}

func (f *fakeAutomation) ValidateSchedule(_ context.Context, _, target uuid.UUID, in runrequests.ScheduleInput) (runrequests.ScheduleChange, uuid.UUID, int64, error) {
	f.validated = append(f.validated, target)
	if f.err != nil {
		return runrequests.ScheduleChange{}, uuid.Nil, 0, f.err
	}
	ch := f.change
	ch.Action = in.Action
	return ch, f.template, 4, nil
}

func (f *fakeAutomation) ApplySchedule(_ context.Context, _ uuid.UUID, r runrequests.Request, p runrequests.PictureSchedule, by uuid.UUID) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied, f.appliedBy, f.appliedPic = append(f.applied, r), by, p
	return nil
}

func (w *wiring) createSchedule(t *testing.T, in runrequests.ScheduleInput) runrequests.Request {
	t.Helper()
	tok := uuid.Must(uuid.NewV7())
	r, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{
		Kind: runrequests.KindSchedule, Schedule: &in, Reason: "nightly safety net",
		RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func scheduleCreate() runrequests.ScheduleInput {
	return runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: &runrequests.ScheduleSpec{Name: "nightly"}}
}

// A create request takes the new schedule's id when it is made, so its
// picture and its approval name the same schedule; its template and
// revision are the validated ones.
func TestCreateScheduleRequestTakesItsTargetID(t *testing.T) {
	w := newWiring()
	r := w.createSchedule(t, scheduleCreate())
	if r.Kind != runrequests.KindSchedule || r.Status != runrequests.StatusPending || r.TargetID == nil || *r.TargetID == r.ID {
		t.Fatalf("request = %+v", r)
	}
	if r.TemplateID != w.auto.template || r.TemplateRevision != 4 || r.Schedule == nil || r.Schedule.Action != runrequests.ScheduleCreate {
		t.Fatalf("request = %+v", r)
	}
	if len(w.auto.validated) != 1 || w.auto.validated[0] != *r.TargetID {
		t.Fatalf("validated %v, target %v", w.auto.validated, *r.TargetID)
	}
	if !r.ExpiresAt.Equal(w.now.Add(time.Hour)) {
		t.Fatalf("expires %v", r.ExpiresAt)
	}
}

// An update or resume request names the schedule it acts on.
func TestUpdateScheduleRequestTargetsTheSchedule(t *testing.T) {
	w := newWiring()
	target := uuid.Must(uuid.NewV7())
	name := "renamed"
	r := w.createSchedule(t, runrequests.ScheduleInput{Action: runrequests.ScheduleUpdate, ScheduleID: target, Patch: &runrequests.SchedulePatch{Name: &name}})
	if *r.TargetID != target || w.auto.validated[0] != target {
		t.Fatalf("target %v, validated %v", *r.TargetID, w.auto.validated)
	}
}

func TestCreateScheduleRequestRefusals(t *testing.T) {
	cases := map[string]runrequests.CreateInput{
		"no schedule":  {Kind: runrequests.KindSchedule, Reason: "x"},
		"sync first":   {Kind: runrequests.KindSchedule, Reason: "x", Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: &runrequests.ScheduleSpec{}}, SyncFirst: true},
		"overrides":    {Kind: runrequests.KindSchedule, Reason: "x", Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: &runrequests.ScheduleSpec{}}, Overrides: runrequests.OverridesInput{CheckMode: true}},
		"bad action":   {Kind: runrequests.KindSchedule, Reason: "x", Schedule: &runrequests.ScheduleInput{Action: "pause"}},
		"spec missing": {Kind: runrequests.KindSchedule, Reason: "x", Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleCreate}},
		"no target":    {Kind: runrequests.KindSchedule, Reason: "x", Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleResume}},
		"no patch":     {Kind: runrequests.KindSchedule, Reason: "x", Schedule: &runrequests.ScheduleInput{Action: runrequests.ScheduleUpdate, ScheduleID: uuid.Must(uuid.NewV7())}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWiring()
			if _, err := w.svc.Create(context.Background(), w.org, in); !errors.Is(err, runrequests.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// A token's schedule requests count against the same cap as its runs.
func TestScheduleRequestsCountAgainstTheTokenCap(t *testing.T) {
	w := newWiring()
	tok := uuid.Must(uuid.NewV7())
	for i := range runrequests.MaxPendingPerToken {
		kind := runrequests.KindRun
		in := runrequests.CreateInput{Kind: kind, TemplateID: uuid.Must(uuid.NewV7()), Reason: "r", RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok}
		if i%2 == 0 {
			sc := scheduleCreate()
			in = runrequests.CreateInput{Kind: runrequests.KindSchedule, Schedule: &sc, Reason: "s", RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok}
		}
		if _, err := w.svc.Create(context.Background(), w.org, in); err != nil {
			t.Fatal(err)
		}
	}
	sc := scheduleCreate()
	_, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{Kind: runrequests.KindSchedule, Schedule: &sc, Reason: "s",
		RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok})
	if !errors.Is(err, runrequests.ErrTooManyPending) {
		t.Fatalf("err = %v, want ErrTooManyPending", err)
	}
}

func (w *wiring) schedulePicture() {
	w.pics.p = runrequests.Picture{TemplateName: "deploy", Schedule: &runrequests.PictureSchedule{
		Action: runrequests.ScheduleCreate, New: &runrequests.ScheduleState{Name: "nightly", Cron: "0 3 * * *"},
	}}
}

// Approving a schedule request applies it, as the approver, exactly as
// pictured; it starts no run, so it takes no run group.
func TestApproveScheduleRequestAppliesItAsTheApprover(t *testing.T) {
	w := newWiring()
	w.schedulePicture()
	r := w.createSchedule(t, scheduleCreate())
	_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	approver := uuid.Must(uuid.NewV7())
	a, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.Request.Status != runrequests.StatusApproved || a.Request.RunGroupID != nil || a.Schedule == nil || a.Schedule.New.Name != "nightly" {
		t.Fatalf("approved = %+v", a)
	}
	if len(w.auto.applied) != 1 || w.auto.applied[0].ID != r.ID || w.auto.appliedBy != approver || w.auto.appliedPic.New.Cron != "0 3 * * *" {
		t.Fatalf("applied = %+v", w.auto)
	}
	if len(w.enq.runs) != 0 {
		t.Fatal("a schedule approval enqueued a run")
	}
	stored, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if stored.RunGroupID != nil || stored.ApprovedFingerprint != fp {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestApproveScheduleRequestRefusals(t *testing.T) {
	approver := uuid.Must(uuid.NewV7())
	t.Run("without templates.write", func(t *testing.T) {
		w := newWiring()
		w.schedulePicture()
		r := w.createSchedule(t, scheduleCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		_, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, runrequests.Approver{Enrol: true})
		if !errors.Is(err, runrequests.ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
		if len(w.auto.applied) != 0 {
			t.Fatal("applied without the permission")
		}
	})
	t.Run("the picture changed", func(t *testing.T) {
		w := newWiring()
		w.schedulePicture()
		r := w.createSchedule(t, scheduleCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		w.pics.p.Schedule.New.Cron = "0 4 * * *"
		if _, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrChanged) {
			t.Fatalf("err = %v, want ErrChanged", err)
		}
	})
	t.Run("the schedule is gone", func(t *testing.T) {
		w := newWiring()
		w.schedulePicture()
		w.pics.p.Schedule.Gone = true
		r := w.createSchedule(t, scheduleCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		if _, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("follow-ups", func(t *testing.T) {
		w := newWiring()
		w.schedulePicture()
		r := w.createSchedule(t, scheduleCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		none := []uuid.UUID{}
		if _, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, &none, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("the run approval", func(t *testing.T) {
		// Approve is the run approval: it holds no templates.write, so a
		// schedule request is refused there.
		w := newWiring()
		w.schedulePicture()
		r := w.createSchedule(t, scheduleCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		if _, err := w.svc.Approve(context.Background(), w.org, r.ID, approver, fp, nil, true); !errors.Is(err, runrequests.ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
	})
}

// A failing apply puts the request back to pending, so the person can
// approve it again once the cause is fixed.
func TestApproveScheduleRequestRevertsWhenTheApplyFails(t *testing.T) {
	w := newWiring()
	w.schedulePicture()
	w.auto.applyErr = errors.New("database down")
	r := w.createSchedule(t, scheduleCreate())
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if _, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, runrequests.Approver{WriteTemplates: true}); err == nil {
		t.Fatal("approve succeeded")
	}
	got, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if got.Status != runrequests.StatusPending || got.ApprovedFingerprint != "" {
		t.Fatalf("after the failure = %+v", got)
	}
}

func TestRejectScheduleRequest(t *testing.T) {
	w := newWiring()
	w.schedulePicture()
	r := w.createSchedule(t, scheduleCreate())
	got, err := w.svc.Reject(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()))
	if err != nil || got.Status != runrequests.StatusRejected {
		t.Fatalf("reject = %+v, %v", got, err)
	}
	if len(w.auto.applied) != 0 {
		t.Fatal("a rejection applied the schedule")
	}
}
