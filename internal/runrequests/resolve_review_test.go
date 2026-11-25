package runrequests_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/triggers"
)

func (f *fakeTriggerSource) EnableAt(_ context.Context, _, id uuid.UUID, revision int64) (triggers.Trigger, error) {
	if f.applyErr != nil {
		return triggers.Trigger{}, f.applyErr
	}
	f.enabled, f.enabledRev = append(f.enabled, id), append(f.enabledRev, revision)
	return triggers.Trigger{ID: id}, nil
}

func (f *fakeTriggerSource) AcceptReviewAt(_ context.Context, _, id uuid.UUID, revision int64) (triggers.Trigger, error) {
	f.accepted, f.acceptedRev = append(f.accepted, id), append(f.acceptedRev, revision)
	return triggers.Trigger{ID: id}, nil
}

func (f *fakeSchedules) AcceptReviewAt(_ context.Context, _, id uuid.UUID, revision int64) (schedules.Schedule, error) {
	f.accepted, f.acceptedRev = append(f.accepted, id), append(f.acceptedRev, revision)
	return schedules.Schedule{ID: id}, nil
}

func reviewChanges(subject assistantchange.Subject, id uuid.UUID) *runrequests.ReviewChange {
	return &runrequests.ReviewChange{Changes: []assistantchange.Change{{Subject: subject, ID: id, Name: "S",
		Fields: []assistantchange.Field{{Name: "limit", Old: "web*", New: "db*"}}}}}
}

// TestReviewPictureAndApplySchedule (spec 2026-11-25 C2): a schedule
// review's picture shows the paused schedule as it is now, whether it is
// still paused for review, and the changes; applying it resumes the
// schedule at the revision the picture showed. A schedule no longer
// paused for review is left as it is; a gone one is refused.
func TestReviewPictureAndApplySchedule(t *testing.T) {
	w := newScheduleWorld()
	s := w.scheds.byID[w.sched]
	s.PausedReason = schedules.PausedForReview
	w.scheds.byID[w.sched] = s
	target := w.sched
	req := runrequests.Request{ID: id(), Kind: runrequests.KindScheduleReview, TemplateID: w.s, TargetID: &target,
		Review: reviewChanges(assistantchange.SubjectTemplate, w.s)}
	ctx := context.Background()
	p, err := w.resolver().Picture(ctx, id(), req)
	if err != nil {
		t.Fatal(err)
	}
	pr := p.Review
	if p.TemplateName != "S" || p.TemplateRevision != 6 || pr == nil || pr.Item != runrequests.ReviewItemSchedule || pr.ItemID != w.sched.String() ||
		pr.Schedule == nil || pr.Schedule.Name != "nightly" || !pr.Schedule.Paused || pr.Schedule.Revision != 5 || !pr.PausedForReview ||
		len(pr.Changes) != 1 || pr.Gone || p.Schedule != nil {
		t.Fatalf("picture = %+v review %+v", p, pr)
	}
	// The stored secret override is masked in the picture.
	if v, ok := pr.Schedule.ExtraVars["db_password"].(map[string]any); !ok || v["$secret"] != "set" {
		t.Fatalf("extra vars = %#v", pr.Schedule.ExtraVars)
	}
	if err := w.resolver().ApplyReview(ctx, id(), req, *pr, id()); err != nil {
		t.Fatal(err)
	}
	if len(w.scheds.resumed) != 1 || w.scheds.resumed[0] != w.sched || w.scheds.resumedRev[0] != 5 {
		t.Fatalf("resumed = %v at %v", w.scheds.resumed, w.scheds.resumedRev)
	}
	// No longer paused for review (a person resumed it, or paused it
	// otherwise): the approval changes nothing about it.
	still := *pr
	still.PausedForReview = false
	if err := w.resolver().ApplyReview(ctx, id(), req, still, id()); err != nil || len(w.scheds.resumed) != 1 {
		t.Fatalf("apply = %v, resumed %v", err, w.scheds.resumed)
	}
	delete(w.scheds.byID, w.sched)
	p, err = w.resolver().Picture(ctx, id(), req)
	if err != nil || !p.Review.Gone || p.Review.Schedule != nil {
		t.Fatalf("gone picture = %+v %v", p.Review, err)
	}
	if err := w.resolver().ApplyReview(ctx, id(), req, *p.Review, id()); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("gone apply = %v", err)
	}
}

// A trigger review shows the trigger (a repository sync trigger with its
// repository and no template) and enables it at the revision shown.
func TestReviewPictureAndApplyTrigger(t *testing.T) {
	w := newTriggerWorld()
	tr := w.trigs.byID[w.sync]
	tr.Enabled, tr.DisabledReason = false, triggers.DisabledForReview
	w.trigs.byID[w.sync] = tr
	target := w.sync
	req := runrequests.Request{ID: id(), Kind: runrequests.KindTriggerReview, TargetID: &target,
		Review: reviewChanges(assistantchange.SubjectRepo, w.repo)}
	ctx := context.Background()
	p, err := w.resolver().Picture(ctx, id(), req)
	if err != nil {
		t.Fatal(err)
	}
	pr := p.Review
	if p.TemplateID != "" || pr.Item != runrequests.ReviewItemTrigger || pr.Trigger == nil || pr.Trigger.Kind != "repo_sync" ||
		pr.Trigger.RepoName == "" || pr.Trigger.Enabled || pr.Trigger.Revision != 2 || !pr.PausedForReview || pr.Gone {
		t.Fatalf("picture = %+v review %+v", p, pr)
	}
	if err := w.resolver().ApplyReview(ctx, id(), req, *pr, id()); err != nil {
		t.Fatal(err)
	}
	if len(w.trigs.enabled) != 1 || w.trigs.enabled[0] != w.sync || w.trigs.enabledRev[0] != 2 {
		t.Fatalf("enabled = %v at %v", w.trigs.enabled, w.trigs.enabledRev)
	}
}

// An assistant cannot resume a schedule, or enable a trigger, that its
// change stopped: the review is a person's. Asking is refused, and so is
// approving a request made before the item was stopped.
func TestAnAssistantCannotResumeWhatItsChangeStopped(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld()
	s := w.scheds.byID[w.sched]
	s.PausedReason = schedules.PausedForReview
	w.scheds.byID[w.sched] = s
	_, _, _, err := w.resolver().ValidateSchedule(ctx, id(), w.sched, runrequests.ScheduleInput{Action: runrequests.ScheduleResume, ScheduleID: w.sched})
	if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "review") {
		t.Fatalf("resume request = %v", err)
	}
	target := w.sched
	req := runrequests.Request{ID: id(), Kind: runrequests.KindSchedule, TemplateID: w.s, TargetID: &target,
		Schedule: &runrequests.ScheduleChange{Action: runrequests.ScheduleResume}}
	p, err := w.resolver().Picture(ctx, id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.resolver().ApplySchedule(ctx, id(), req, *p.Schedule, id()); !errors.Is(err, runrequests.ErrInvalidInput) || len(w.scheds.resumed) != 0 {
		t.Fatalf("approved resume = %v, resumed %v", err, w.scheds.resumed)
	}

	tw := newTriggerWorld()
	tr := tw.trigs.byID[tw.run]
	tr.DisabledReason = triggers.DisabledForReview
	tw.trigs.byID[tw.run] = tr
	on := true
	_, _, _, err = tw.resolver().ValidateTrigger(ctx, id(), tw.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: tw.run,
		Patch: &runrequests.TriggerPatch{Enabled: &on}})
	if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "review") {
		t.Fatalf("enable request = %v", err)
	}
	// An update that leaves enabled out keeps it disabled: allowed.
	name := "renamed"
	if _, _, _, err := tw.resolver().ValidateTrigger(ctx, id(), tw.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: tw.run,
		Patch: &runrequests.TriggerPatch{Name: &name}}); err != nil {
		t.Fatalf("rename = %v", err)
	}
}

// Fix round 1: the review of an item a person had stopped before the
// change names that person, and approving it leaves the item stopped (the
// reason goes) instead of resuming it.
func TestReviewOfAnItemAPersonStopped(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld()
	s := w.scheds.byID[w.sched]
	s.PausedReason = schedules.PausedForReview
	w.scheds.byID[w.sched] = s
	person := id()
	target := w.sched
	ch := reviewChanges(assistantchange.SubjectTemplate, w.s)
	ch.PausedBefore = &runrequests.PausedBefore{By: &person}
	req := runrequests.Request{ID: id(), Kind: runrequests.KindScheduleReview, TemplateID: w.s, TargetID: &target, Review: ch}
	p, err := w.resolver().Picture(ctx, id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Review.PausedBefore == nil || p.Review.PausedBefore.ByUserID != person.String() {
		t.Fatalf("paused before = %+v", p.Review.PausedBefore)
	}
	if err := w.resolver().ApplyReview(ctx, id(), req, *p.Review, id()); err != nil {
		t.Fatal(err)
	}
	if len(w.scheds.resumed) != 0 || len(w.scheds.accepted) != 1 || w.scheds.acceptedRev[0] != 5 {
		t.Fatalf("resumed %v, accepted %v at %v", w.scheds.resumed, w.scheds.accepted, w.scheds.acceptedRev)
	}

	tw := newTriggerWorld()
	tr := tw.trigs.byID[tw.run]
	tr.DisabledReason = triggers.DisabledForReview
	tw.trigs.byID[tw.run] = tr
	run := tw.run
	treq := runrequests.Request{ID: id(), Kind: runrequests.KindTriggerReview, TemplateID: tw.x, TargetID: &run,
		Review: &runrequests.ReviewChange{Changes: ch.Changes, PausedBefore: &runrequests.PausedBefore{}}}
	tp, err := tw.resolver().Picture(ctx, id(), treq)
	if err != nil || tp.Review.PausedBefore == nil || tp.Review.PausedBefore.ByUserID != "" {
		t.Fatalf("trigger picture = %+v, %v", tp.Review, err)
	}
	if err := tw.resolver().ApplyReview(ctx, id(), treq, *tp.Review, id()); err != nil {
		t.Fatal(err)
	}
	if len(tw.trigs.enabled) != 0 || len(tw.trigs.accepted) != 1 || tw.trigs.acceptedRev[0] != 4 {
		t.Fatalf("enabled %v, accepted %v", tw.trigs.enabled, tw.trigs.accepted)
	}
}
