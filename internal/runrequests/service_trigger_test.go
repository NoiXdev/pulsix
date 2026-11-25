package runrequests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/triggers"
)

// fakeTriggers stands in for the resolver's trigger side.
type fakeTriggers struct {
	err        error
	applyErr   error
	kind       triggers.Kind
	secret     string
	validated  []uuid.UUID
	applied    []runrequests.Request
	appliedBy  uuid.UUID
	appliedPic runrequests.PictureTrigger
}

func (f *fakeAutomation) ValidateTrigger(_ context.Context, _, target uuid.UUID, in runrequests.TriggerInput) (runrequests.TriggerChange, uuid.UUID, int64, error) {
	t := &f.triggers
	t.validated = append(t.validated, target)
	if t.err != nil {
		return runrequests.TriggerChange{}, uuid.Nil, 0, t.err
	}
	kind := t.kind
	if kind == "" {
		kind = triggers.KindTemplateRun
	}
	tmpl := f.template
	if kind == triggers.KindRepoSync {
		tmpl = uuid.Nil
	}
	return runrequests.TriggerChange{Action: in.Action, Kind: kind, Spec: in.Spec}, tmpl, 3, nil
}

func (f *fakeAutomation) ApplyTrigger(_ context.Context, _ uuid.UUID, r runrequests.Request, p runrequests.PictureTrigger, by uuid.UUID) (string, error) {
	t := &f.triggers
	if t.applyErr != nil {
		return "", t.applyErr
	}
	t.applied, t.appliedBy, t.appliedPic = append(t.applied, r), by, p
	return t.secret, nil
}

func (w *wiring) createTrigger(t *testing.T, in runrequests.TriggerInput) runrequests.Request {
	t.Helper()
	tok := uuid.Must(uuid.NewV7())
	r, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{
		Kind: runrequests.KindTrigger, Trigger: &in, Reason: "deploy on push",
		RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func triggerCreate() runrequests.TriggerInput {
	return runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: &runrequests.TriggerSpec{Name: "push", Kind: triggers.KindTemplateRun}}
}

func (w *wiring) triggerPicture(action runrequests.TriggerAction) {
	w.pics.p = runrequests.Picture{TemplateName: "deploy", Trigger: &runrequests.PictureTrigger{
		Action: action, New: &runrequests.TriggerState{Name: "push", Enabled: true},
	}}
}

// A create request takes the new trigger's id when it is made, so the
// picture's URL and the approval name the same trigger.
func TestCreateTriggerRequestTakesItsTargetID(t *testing.T) {
	w := newWiring()
	r := w.createTrigger(t, triggerCreate())
	if r.Kind != runrequests.KindTrigger || r.TargetID == nil || *r.TargetID == r.ID || r.Trigger == nil || r.Trigger.Action != runrequests.TriggerCreate {
		t.Fatalf("request = %+v", r)
	}
	if r.TemplateID != w.auto.template || r.TemplateRevision != 3 || w.auto.triggers.validated[0] != *r.TargetID {
		t.Fatalf("request = %+v, validated %v", r, w.auto.triggers.validated)
	}
}

// A repository sync trigger's request names no template.
func TestRepoSyncTriggerRequestHasNoTemplate(t *testing.T) {
	w := newWiring()
	w.auto.triggers.kind = triggers.KindRepoSync
	r := w.createTrigger(t, triggerCreate())
	if r.TemplateID != uuid.Nil || r.Trigger.Kind != triggers.KindRepoSync {
		t.Fatalf("request = %+v", r)
	}
}

func TestCreateTriggerRequestRefusals(t *testing.T) {
	spec := &runrequests.TriggerSpec{Name: "push"}
	id := uuid.Must(uuid.NewV7())
	cases := map[string]runrequests.CreateInput{
		"no trigger":       {Kind: runrequests.KindTrigger, Reason: "x"},
		"sync first":       {Kind: runrequests.KindTrigger, Reason: "x", Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: spec}, SyncFirst: true},
		"overrides":        {Kind: runrequests.KindTrigger, Reason: "x", Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: spec}, Overrides: runrequests.OverridesInput{CheckMode: true}},
		"bad action":       {Kind: runrequests.KindTrigger, Reason: "x", Trigger: &runrequests.TriggerInput{Action: "disable", TriggerID: id}},
		"spec missing":     {Kind: runrequests.KindTrigger, Reason: "x", Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerCreate}},
		"no target":        {Kind: runrequests.KindTrigger, Reason: "x", Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerRotate}},
		"no patch":         {Kind: runrequests.KindTrigger, Reason: "x", Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: id}},
		"run with trigger": {Kind: runrequests.KindRun, TemplateID: id, Reason: "x", Trigger: &runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: spec}},
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

// Approving a trigger request applies it as the approver, exactly as
// pictured, and hands the new secret to the approval only: the stored
// request keeps none.
func TestApproveTriggerRequestReturnsTheSecretOnce(t *testing.T) {
	w := newWiring()
	w.triggerPicture(runrequests.TriggerCreate)
	w.auto.triggers.secret = "s3cr3t-value"
	r := w.createTrigger(t, triggerCreate())
	_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	approver := uuid.Must(uuid.NewV7())
	a, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.Request.Status != runrequests.StatusApproved || a.Request.RunGroupID != nil || a.Trigger == nil || a.TriggerSecret != "s3cr3t-value" {
		t.Fatalf("approved = %+v", a)
	}
	ts := w.auto.triggers
	if len(ts.applied) != 1 || ts.applied[0].ID != r.ID || ts.appliedBy != approver || ts.appliedPic.New.Name != "push" {
		t.Fatalf("applied = %+v", ts)
	}
	stored, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if stored.Status != runrequests.StatusApproved || stored.ApprovedFingerprint != fp || stored.Trigger.Spec.Name != "push" {
		t.Fatalf("stored = %+v", stored)
	}
	if len(w.enq.runs) != 0 {
		t.Fatal("a trigger approval enqueued a run")
	}
}

func TestApproveTriggerRequestRefusals(t *testing.T) {
	approver := uuid.Must(uuid.NewV7())
	approve := func(w *wiring, r runrequests.Request, may runrequests.Approver) error {
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		_, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, may)
		return err
	}
	t.Run("a template run trigger without templates.write", func(t *testing.T) {
		w := newWiring()
		w.triggerPicture(runrequests.TriggerCreate)
		r := w.createTrigger(t, triggerCreate())
		if err := approve(w, r, runrequests.Approver{WriteRepos: true}); !errors.Is(err, runrequests.ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
	})
	t.Run("a repository sync trigger without repos.write", func(t *testing.T) {
		w := newWiring()
		w.auto.triggers.kind = triggers.KindRepoSync
		w.triggerPicture(runrequests.TriggerCreate)
		r := w.createTrigger(t, triggerCreate())
		if err := approve(w, r, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
		if err := approve(w, r, runrequests.Approver{WriteRepos: true}); err != nil {
			t.Fatalf("with repos.write: %v", err)
		}
	})
	t.Run("the picture changed", func(t *testing.T) {
		w := newWiring()
		w.triggerPicture(runrequests.TriggerCreate)
		r := w.createTrigger(t, triggerCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		w.pics.p.TemplateRevision++
		if _, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, nil, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrChanged) {
			t.Fatalf("err = %v, want ErrChanged", err)
		}
	})
	t.Run("the trigger is gone", func(t *testing.T) {
		w := newWiring()
		w.triggerPicture(runrequests.TriggerRotate)
		w.pics.p.Trigger.Gone = true
		r := w.createTrigger(t, runrequests.TriggerInput{Action: runrequests.TriggerRotate, TriggerID: uuid.Must(uuid.NewV7())})
		if err := approve(w, r, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("follow-ups", func(t *testing.T) {
		w := newWiring()
		w.triggerPicture(runrequests.TriggerCreate)
		r := w.createTrigger(t, triggerCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		none := []uuid.UUID{}
		if _, err := w.svc.ApproveWith(context.Background(), w.org, r.ID, approver, fp, &none, runrequests.Approver{WriteTemplates: true}); !errors.Is(err, runrequests.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("the run approval", func(t *testing.T) {
		w := newWiring()
		w.triggerPicture(runrequests.TriggerCreate)
		r := w.createTrigger(t, triggerCreate())
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		if _, err := w.svc.Approve(context.Background(), w.org, r.ID, approver, fp, nil, true); !errors.Is(err, runrequests.ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
	})
	t.Run("a failing apply leaves the request pending", func(t *testing.T) {
		w := newWiring()
		w.triggerPicture(runrequests.TriggerCreate)
		w.auto.triggers.applyErr = errors.New("database down")
		r := w.createTrigger(t, triggerCreate())
		if err := approve(w, r, runrequests.Approver{WriteTemplates: true}); err == nil {
			t.Fatal("approve succeeded")
		}
		got, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
		if got.Status != runrequests.StatusPending || got.ApprovedFingerprint != "" {
			t.Fatalf("after the failure = %+v", got)
		}
	})
}
