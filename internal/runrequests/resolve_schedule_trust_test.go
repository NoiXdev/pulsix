package runrequests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

// An update's patch keeps what it leaves out and switches the mode with
// the timetable it gives.
func TestValidateScheduleUpdatePatches(t *testing.T) {
	cron, empty, tz, name := "*/5 * * * *", "", "UTC", "renamed"
	modeCron, modeSimple := schedules.ModeCron, schedules.ModeSimple
	weekly := &cronexpr.Simple{Kind: "weekly", Days: []string{"mon"}, Time: "04:00"}
	guards := schedules.Guards{Overlap: schedules.OverlapQueue}
	cases := []struct {
		name  string
		patch runrequests.SchedulePatch
		check func(t *testing.T, w *scheduleWorld, s *runrequests.ScheduleSpec)
	}{
		{"nothing", runrequests.SchedulePatch{}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeSimple || s.Cron != "" || s.Simple == nil || s.Simple.Kind != "daily" || s.Name != "nightly" {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"cron mode keeps the compiled timetable", runrequests.SchedulePatch{Mode: &modeCron}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeCron || s.Cron != "0 3 * * *" || s.Simple != nil {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"a cron switches to cron mode", runrequests.SchedulePatch{Cron: &cron}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeCron || s.Cron != cron || s.Simple != nil {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"a simple shape switches to simple mode", runrequests.SchedulePatch{Simple: weekly}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeSimple || s.Cron != "" || s.Simple.Kind != "weekly" {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"name, zone, guards, template", runrequests.SchedulePatch{Name: &name, Timezone: &tz, Guards: &guards}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Name != name || s.Timezone != tz || s.Guards.Overlap != schedules.OverlapQueue {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"an empty limit clears it", runrequests.SchedulePatch{Limit: &empty}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Limit != nil {
				t.Fatalf("limit = %q", *s.Limit)
			}
		}},
		{"the template stays", runrequests.SchedulePatch{}, func(t *testing.T, w *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.TemplateID != w.s {
				t.Fatalf("template = %v", s.TemplateID)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newScheduleWorld()
			patch := tc.patch
			ch, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &patch})
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, w, ch.Spec)
		})
	}
	// A template the update moves to is the request's template.
	w := newScheduleWorld()
	_, tmpl, rev, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &runrequests.SchedulePatch{TemplateID: &w.y},
	})
	if err != nil || tmpl != w.y || rev != 5 {
		t.Fatalf("moved to %v rev %d, %v", tmpl, rev, err)
	}
}

// A kept secret nested in a value shows masked in the picture, never as
// the keep marker.
func TestPictureMasksKeptSecrets(t *testing.T) {
	w := newScheduleWorld()
	spec := w.spec()
	spec.ExtraVars = map[string]any{"list": []any{map[string]any{"$secret": "keep", "from": "x"}}}
	p, err := w.resolver().Picture(context.Background(), id(), w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleCreate, Spec: spec}, id()))
	if err != nil {
		t.Fatal(err)
	}
	item := p.Schedule.New.ExtraVars["list"].([]any)[0].(map[string]any)
	if item["$secret"] != "set" || len(item) != 1 {
		t.Fatalf("shown = %#v", item)
	}
}

// The trust of values an update copies from the schedule is decided when
// the request is made and travels with it (review, finding 1): a later
// clear of the schedule's overrides cannot turn the copied assistant's
// values into trusted ones, and a copied assistant's value is checked
// against the template the update moves to.
func TestUpdateCarriesTheTrustOfCopiedValues(t *testing.T) {
	w := newScheduleWorld()
	s := w.scheds.byID[w.sched]
	s.UntrustedExtraVars = true
	w.scheds.byID[w.sched] = s
	cron := "*/30 * * * *"
	ch, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &runrequests.SchedulePatch{Cron: &cron},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.AssistantExtraVars || ch.AssistantLimit || !w.scheds.validated[0].UntrustedExtraVars {
		t.Fatalf("change = %+v, validated %+v", ch, w.scheds.validated[0])
	}
	// The schedule's overrides were cleared since: the approval stores the
	// request's copied values with the request's mark.
	s.UntrustedExtraVars, s.Overrides = false, templates.Overrides{}
	w.scheds.byID[w.sched] = s
	req := w.request(ch, w.sched)
	if err := w.resolver().ApplySchedule(context.Background(), id(), req, runrequests.PictureSchedule{Current: &runrequests.ScheduleState{Revision: 5}}, id()); err != nil {
		t.Fatal(err)
	}
	if !w.scheds.updated[0].UntrustedExtraVars || w.scheds.updated[0].Overrides.ExtraVars["release"] != "1.0" {
		t.Fatalf("updated = %+v", w.scheds.updated[0])
	}
	// Moving the copied assistant's values to another template checks
	// them there: its targets' inventory holds a secret named release.
	w2 := newScheduleWorld()
	s2 := w2.scheds.byID[w2.sched]
	s2.UntrustedExtraVars = true
	w2.scheds.byID[w2.sched] = s2
	w2.tmpls.fakeTemplates[w2.x] = w2.tmpls.fakeTemplates[w2.s]
	w2.invSecrets.names["release"] = true
	_, _, _, err = w2.resolver().ValidateSchedule(context.Background(), id(), w2.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w2.sched, Patch: &runrequests.SchedulePatch{TemplateID: &w2.x},
	})
	if !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("a copied assistant's value shadowing a secret: %v", err)
	}
}

// Each part carries its own mark: an assistant's limit leaves a person's
// extra vars trusted (controller ruling, finding 2).
func TestScheduleTrustPerPart(t *testing.T) {
	w := newScheduleWorld()
	limit := "db*"
	ch, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &runrequests.SchedulePatch{Limit: &limit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.AssistantLimit || ch.AssistantExtraVars {
		t.Fatalf("change = %+v", ch)
	}
	in := w.scheds.validated[0]
	if !in.UntrustedLimit || in.UntrustedExtraVars {
		t.Fatalf("validated = %+v", in)
	}
	p, err := w.resolver().Picture(context.Background(), id(), w.request(ch, w.sched))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Schedule.AssistantLimit || p.Schedule.AssistantExtraVars {
		t.Fatalf("picture = %+v", p.Schedule)
	}
	// A person's extra var with a name an assistant may not set does not
	// fail the assistant's limit-only request.
	s := w.scheds.byID[w.sched]
	s.Overrides.ExtraVars = map[string]any{"hosts": "all"}
	w.scheds.byID[w.sched] = s
	if _, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &runrequests.SchedulePatch{Limit: &limit},
	}); err != nil {
		t.Fatalf("a person's extra vars were held to the assistant's rules: %v", err)
	}
}

// A request of a kind this version does not know is no run picture.
func TestPictureRefusesAnUnknownKind(t *testing.T) {
	w := newScheduleWorld()
	_, err := w.resolver().Picture(context.Background(), id(), runrequests.Request{ID: id(), Kind: "trigger", TemplateID: w.x})
	if !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestApplyResumeOfAGoneSchedule(t *testing.T) {
	w := newScheduleWorld()
	res := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleResume}, w.sched)
	if err := w.resolver().ApplySchedule(context.Background(), id(), res, runrequests.PictureSchedule{Action: runrequests.ScheduleResume}, id()); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("err = %v", err)
	}
}
