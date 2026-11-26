package runrequests_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/triggers"
)

func intp(v int) *int { return &v }

// settlingRun gives the world's template run trigger a settle delay of 30
// seconds with a maximum wait of 600.
func (w *triggerWorld) settlingRun() {
	stored := w.trigs.byID[w.run]
	stored.SettleSeconds, stored.MaxWaitSeconds = 30, intp(600)
	w.trigs.byID[w.run] = stored
}

// An assistant's change that does not name the settle delay keeps it, in
// the request, its picture and what the approval stores (backlog 2.137):
// rebuilding the trigger from the patch never silently turns it off.
func TestTriggerUpdateKeepsTheSettleDelay(t *testing.T) {
	w := newTriggerWorld()
	w.settlingRun()
	r := w.resolver()
	name := "renamed"
	ch, _, _, err := r.ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &runrequests.TriggerPatch{Name: &name}})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Spec.SettleSeconds != 30 || ch.Spec.MaxWaitSeconds == nil || *ch.Spec.MaxWaitSeconds != 600 {
		t.Fatalf("spec = %+v", ch.Spec)
	}
	if v := w.trigs.validated[len(w.trigs.validated)-1]; v.SettleSeconds != 30 || v.MaxWaitSeconds == nil || *v.MaxWaitSeconds != 600 {
		t.Fatalf("validated = %+v", v)
	}
	req := triggerRequest(triggers.KindTemplateRun, ch, w.run, w.x)
	p, err := r.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	for which, s := range map[string]*runrequests.TriggerState{"current": p.Trigger.Current, "new": p.Trigger.New} {
		if s.SettleSeconds != 30 || s.MaxWaitSeconds == nil || *s.MaxWaitSeconds != 600 {
			t.Fatalf("%s pictured = %+v", which, s)
		}
	}
	if _, err := r.ApplyTrigger(context.Background(), id(), req, *p.Trigger, id()); err != nil {
		t.Fatal(err)
	}
	if u := w.trigs.updated[0]; u.SettleSeconds != 30 || u.MaxWaitSeconds == nil || *u.MaxWaitSeconds != 600 || u.Name != "renamed" {
		t.Fatalf("applied = %+v", u)
	}
}

// A change of the settle delay is pictured and applied as asked: a new
// delay keeps the maximum wait, a maximum wait of 0 is the default,
// turning the delay off drops the maximum wait.
func TestTriggerUpdateChangesTheSettleDelay(t *testing.T) {
	cases := []struct {
		name        string
		patch       runrequests.TriggerPatch
		wantSettle  int
		wantMaxWait *int
	}{
		{"new delay", runrequests.TriggerPatch{SettleSeconds: intp(60)}, 60, intp(600)},
		{"default maximum wait", runrequests.TriggerPatch{MaxWaitSeconds: intp(0)}, 30, nil},
		{"off", runrequests.TriggerPatch{SettleSeconds: intp(0)}, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTriggerWorld()
			w.settlingRun()
			r := w.resolver()
			ch, _, _, err := r.ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &tc.patch})
			if err != nil {
				t.Fatal(err)
			}
			req := triggerRequest(triggers.KindTemplateRun, ch, w.run, w.x)
			p, err := r.Picture(context.Background(), id(), req)
			if err != nil {
				t.Fatal(err)
			}
			if n := p.Trigger.New; n.SettleSeconds != tc.wantSettle || !sameIntPtr(n.MaxWaitSeconds, tc.wantMaxWait) {
				t.Fatalf("pictured = %d %v", n.SettleSeconds, n.MaxWaitSeconds)
			}
			if _, err := r.ApplyTrigger(context.Background(), id(), req, *p.Trigger, id()); err != nil {
				t.Fatal(err)
			}
			if u := w.trigs.updated[0]; u.SettleSeconds != tc.wantSettle || !sameIntPtr(u.MaxWaitSeconds, tc.wantMaxWait) {
				t.Fatalf("applied = %d %v", u.SettleSeconds, u.MaxWaitSeconds)
			}
		})
	}
}

// A patch that names the settle delay the trigger already has changes
// nothing; a create carries the settle delay to the trigger service.
func TestTriggerRequestSettleNoOpAndCreate(t *testing.T) {
	w := newTriggerWorld()
	w.settlingRun()
	r := w.resolver()
	_, _, _, err := r.ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run,
		Patch: &runrequests.TriggerPatch{SettleSeconds: intp(30), MaxWaitSeconds: intp(600)}})
	if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("same settle delay: %v", err)
	}
	spec := w.runSpec()
	spec.SettleSeconds, spec.MaxWaitSeconds = 20, intp(120)
	ch, _, _, err := r.ValidateTrigger(context.Background(), id(), id(), runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: spec})
	if err != nil || ch.Spec.SettleSeconds != 20 || *ch.Spec.MaxWaitSeconds != 120 {
		t.Fatalf("create = %+v %v", ch.Spec, err)
	}
	target := id()
	req := triggerRequest(triggers.KindTemplateRun, ch, target, w.x)
	if _, err := r.ApplyTrigger(context.Background(), id(), req, runrequests.PictureTrigger{Action: runrequests.TriggerCreate}, id()); err != nil {
		t.Fatal(err)
	}
	if c := w.trigs.created[0]; c.SettleSeconds != 20 || *c.MaxWaitSeconds != 120 {
		t.Fatalf("created = %+v", c)
	}
}

func sameIntPtr(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
