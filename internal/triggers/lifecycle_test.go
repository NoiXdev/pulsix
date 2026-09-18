package triggers_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/triggers"
)

func TestServiceListDeleteAndDeliveries(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	if list, err := w.svc.List(ctx, w.org, triggers.ListFilter{TemplateID: &w.template}); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if _, err := w.svc.Deliveries(ctx, w.org, c.Trigger.ID, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Deliveries(ctx, uuid.Must(uuid.NewV7()), c.Trigger.ID, 50); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("another org's deliveries: %v", err)
	}
	if _, err := w.svc.RotateSecret(ctx, w.org, uuid.Must(uuid.NewV7())); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("rotate unknown: %v", err)
	}
	if err := w.svc.Delete(ctx, w.org, c.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Get(ctx, w.org, c.Trigger.ID); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestReceiveRepoSyncSkipAndFailure(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, triggers.Input{Kind: triggers.KindRepoSync, RepoID: &rw.repo, Name: "sync", Enabled: true,
		Preset: triggers.PresetBearer, OnActive: triggers.OnActiveSkip})
	if err != nil {
		t.Fatal(err)
	}
	rw.runs.active = true
	if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeSkippedActive {
		t.Fatalf("skip = %+v", res)
	}
	rw.runs.active = false
	for cause, reason := range map[error]string{
		jobs.ErrNoZone:         "the organization has no default zone",
		jobs.ErrHostKeyChanged: "every targeted host has a changed host key",
		errors.New("boom"):     "could not enqueue (see the manager log)",
	} {
		rw.runs.err = cause
		if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeFailed {
			t.Fatalf("%v: %+v", cause, res)
		}
		if last := rw.rec.deliveries[len(rw.rec.deliveries)-1]; last.Reason != reason {
			t.Fatalf("%v: reason %q", cause, last.Reason)
		}
	}
}

func TestStoreUpdateSecretListDelete(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-lifecycle")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit)
	created, err := st.Create(scoped, in, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	created.Enabled = false
	if _, err := st.Update(scoped, created, created.Revision+1); !errors.Is(err, triggers.ErrRevisionMismatch) {
		t.Fatalf("stale update: %v", err)
	}
	up, err := st.Update(scoped, created, created.Revision)
	if err != nil || up.Enabled || up.Revision != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	unknown := created
	unknown.ID = uuid.Must(uuid.NewV7())
	if _, err := st.Update(scoped, unknown, 1); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
	if _, err := st.SetSecret(scoped, org, created.ID, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if _, secret, _ := st.Resolve(ctx, created.ID); string(secret) != "two" {
		t.Fatalf("secret after rotation = %q", secret)
	}
	if _, err := st.SetSecret(scoped, org, uuid.Must(uuid.NewV7()), []byte("x")); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("set secret unknown: %v", err)
	}
	if list, err := st.List(scoped, org, triggers.ListFilter{TemplateID: &tmpl}); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	other := uuid.Must(uuid.NewV7())
	if list, _ := st.List(scoped, org, triggers.ListFilter{RepoID: &other}); len(list) != 0 {
		t.Fatalf("repo filter = %v", list)
	}
	if err := st.Delete(scoped, org, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(scoped, org, created.ID); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
