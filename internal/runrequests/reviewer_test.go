package runrequests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// memReviews is the review part of the store.
var ctx = context.Background()

type memReviews struct {
	rows []runrequests.Request
}

func (m *memReviews) Insert(_ context.Context, r runrequests.Request) error {
	m.rows = append(m.rows, r)
	return nil
}

func (m *memReviews) OpenReview(_ context.Context, orgID uuid.UUID, kind runrequests.Kind, target uuid.UUID, now time.Time) (runrequests.Request, error) {
	for _, r := range m.rows {
		if r.OrgID == orgID && r.Kind == kind && *r.TargetID == target && r.Status == runrequests.StatusPending && now.Before(r.ExpiresAt) {
			return r, nil
		}
	}
	return runrequests.Request{}, runrequests.ErrNotFound
}

func (m *memReviews) RefreshReview(_ context.Context, _, id uuid.UUID, change runrequests.ReviewChange, expiresAt time.Time, rev int64) error {
	for i, r := range m.rows {
		if r.ID == id {
			c := change
			m.rows[i].Review, m.rows[i].ExpiresAt, m.rows[i].TemplateRevision = &c, expiresAt, rev
		}
	}
	return nil
}

func (m *memReviews) CloseReviews(_ context.Context, _ uuid.UUID, kind runrequests.Kind, target, by uuid.UUID, at time.Time) error {
	for i, r := range m.rows {
		if r.Kind == kind && *r.TargetID == target && r.Status == runrequests.StatusPending {
			m.rows[i].Status, m.rows[i].DecidedBy, m.rows[i].DecidedAt = runrequests.StatusApproved, &by, &at
		}
	}
	return nil
}

type reviewWorld struct {
	org, tmplA, tmplB, repo uuid.UUID
	schedules               map[uuid.UUID]schedules.Schedule
	triggers                map[uuid.UUID]triggers.Trigger
	events                  []audit.Event
	reviews                 *memReviews
	reviewer                *runrequests.Reviewer
	now                     time.Time
}

func (w *reviewWorld) Get(_ context.Context, _, id uuid.UUID) (templates.Template, error) {
	switch id {
	case w.tmplA:
		return templates.Template{ID: id, Name: "deploy", Revision: 4}, nil
	case w.tmplB:
		return templates.Template{ID: id, Name: "cleanup", Revision: 9}, nil
	}
	return templates.Template{}, templates.ErrNotFound
}

func (w *reviewWorld) IDsOfRepo(_ context.Context, _, repo uuid.UUID) ([]uuid.UUID, error) {
	if repo == w.repo {
		return []uuid.UUID{w.tmplA, w.tmplB}, nil
	}
	return nil, nil
}

func (w *reviewWorld) ListOfTemplates(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]schedules.Schedule, error) {
	var out []schedules.Schedule
	for _, id := range ids {
		for _, s := range w.schedules {
			if s.TemplateID == id {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func (w *reviewWorld) PauseForReview(_ context.Context, _, id uuid.UUID) (schedules.Schedule, bool, error) {
	s := w.schedules[id]
	if s.Paused {
		return s, false, nil
	}
	s.Paused, s.PausedReason, s.Revision = true, schedules.PausedForReview, s.Revision+1
	w.schedules[id] = s
	return s, true, nil
}

func (w *reviewWorld) ListForReview(_ context.Context, _ uuid.UUID, ids []uuid.UUID, repo *uuid.UUID) ([]triggers.Trigger, error) {
	var out []triggers.Trigger
	for _, t := range w.triggers {
		hit := repo != nil && t.RepoID != nil && *t.RepoID == *repo
		for _, id := range ids {
			hit = hit || (t.TemplateID != nil && *t.TemplateID == id)
		}
		if hit {
			out = append(out, t)
		}
	}
	return out, nil
}

func (w *reviewWorld) DisableForReview(_ context.Context, _, id uuid.UUID) (triggers.Trigger, bool, error) {
	t := w.triggers[id]
	if !t.Enabled {
		return t, false, nil
	}
	t.Enabled, t.DisabledReason, t.Revision = false, triggers.DisabledForReview, t.Revision+1
	w.triggers[id] = t
	return t, true, nil
}

func (w *reviewWorld) Record(_ context.Context, e audit.Event) error {
	w.events = append(w.events, e)
	return nil
}

func newReviewWorld() *reviewWorld {
	w := &reviewWorld{
		org: uuid.Must(uuid.NewV7()), tmplA: uuid.Must(uuid.NewV7()), tmplB: uuid.Must(uuid.NewV7()), repo: uuid.Must(uuid.NewV7()),
		schedules: map[uuid.UUID]schedules.Schedule{}, triggers: map[uuid.UUID]triggers.Trigger{},
		reviews: &memReviews{}, now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
	}
	w.reviewer = runrequests.NewReviewer(w.reviews, runrequests.ReviewSources{Templates: w, Schedules: w, Triggers: w}, w,
		func() time.Time { return w.now }, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
	return w
}

func (w *reviewWorld) schedule(name string, tmpl uuid.UUID, paused bool, reason string) uuid.UUID {
	id := uuid.Must(uuid.NewV7())
	w.schedules[id] = schedules.Schedule{ID: id, OrgID: w.org, TemplateID: tmpl, Name: name, Paused: paused, PausedReason: reason, Revision: 1}
	return id
}

func (w *reviewWorld) trigger(name string, tmpl, repo *uuid.UUID, enabled bool) uuid.UUID {
	id := uuid.Must(uuid.NewV7())
	kind := triggers.KindTemplateRun
	if repo != nil {
		kind = triggers.KindRepoSync
	}
	w.triggers[id] = triggers.Trigger{ID: id, OrgID: w.org, Kind: kind, TemplateID: tmpl, RepoID: repo, Name: name, Enabled: enabled, Revision: 1}
	return id
}

func (w *reviewWorld) reviewOf(target uuid.UUID) []runrequests.Request {
	var out []runrequests.Request
	for _, r := range w.reviews.rows {
		if *r.TargetID == target {
			out = append(out, r)
		}
	}
	return out
}

// TestAnAssistantsTemplateChangePausesWhatRunsIt (spec 2026-11-25 C2):
// every active schedule of the template is paused and every enabled
// trigger that runs it disabled, each with one review naming the change and
// the token, and an activity entry. A schedule a person paused, a disabled
// trigger and the items of another template are left alone.
func TestAnAssistantsTemplateChangePausesWhatRunsIt(t *testing.T) {
	w := newReviewWorld()
	active := w.schedule("nightly", w.tmplA, false, "")
	byPerson := w.schedule("weekly", w.tmplA, true, "")
	other := w.schedule("other", w.tmplB, false, "")
	hook := w.trigger("deploy-hook", &w.tmplA, nil, true)
	off := w.trigger("old-hook", &w.tmplA, nil, false)
	token := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	change := assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA, Name: "deploy", UserID: user, TokenID: &token,
		Fields: []assistantchange.Field{{Name: "limit", Old: "web*", New: "db*"}}}
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if s := w.schedules[active]; !s.Paused || s.PausedReason != schedules.PausedForReview {
		t.Fatalf("active schedule = %+v", s)
	}
	if w.schedules[byPerson].PausedReason != "" || w.schedules[other].Paused || w.triggers[off].Revision != 1 || w.triggers[off].DisabledReason != "" {
		t.Fatalf("left alone: %+v %+v %+v", w.schedules[byPerson], w.schedules[other], w.triggers[off])
	}
	if tr := w.triggers[hook]; tr.Enabled || tr.DisabledReason != triggers.DisabledForReview {
		t.Fatalf("trigger = %+v", tr)
	}
	if len(w.reviews.rows) != 2 {
		t.Fatalf("reviews = %+v", w.reviews.rows)
	}
	sr := w.reviewOf(active)
	if len(sr) != 1 || sr[0].Kind != runrequests.KindScheduleReview || sr[0].TemplateID != w.tmplA || sr[0].TemplateRevision != 4 ||
		sr[0].RequestedByUser != user || *sr[0].RequestedByToken != token || sr[0].Status != runrequests.StatusPending ||
		!sr[0].ExpiresAt.Equal(w.now.Add(runrequests.ReviewTTL)) || len(sr[0].Review.Changes) != 1 || sr[0].Reason == "" {
		t.Fatalf("schedule review = %+v", sr)
	}
	if tr := w.reviewOf(hook); len(tr) != 1 || tr[0].Kind != runrequests.KindTriggerReview {
		t.Fatalf("trigger review = %+v", tr)
	}
	if len(w.events) != 2 || w.events[0].Action != "pauseScheduleForReview" || w.events[0].ObjectID != active.String() ||
		w.events[0].ActorKind != audit.ActorKindToken || *w.events[0].TokenID != token || *w.events[0].ActorID != user ||
		w.events[1].Action != "disableTriggerForReview" || w.events[1].ObjectID != hook.String() {
		t.Fatalf("events = %+v", w.events)
	}

	// A second change of the same template refreshes the open reviews: one
	// per item, listing both changes; nothing is paused twice.
	w.now = w.now.Add(time.Hour)
	change.Fields = []assistantchange.Field{{Name: "agent_enrollment", Old: false, New: true}}
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if len(w.reviews.rows) != 2 || len(w.events) != 2 {
		t.Fatalf("after the second change: %d reviews, %d events", len(w.reviews.rows), len(w.events))
	}
	if sr := w.reviewOf(active); len(sr[0].Review.Changes) != 2 || !sr[0].ExpiresAt.Equal(w.now.Add(runrequests.ReviewTTL)) {
		t.Fatalf("refreshed = %+v", sr[0])
	}
	// A review a person decided (closed) is not refreshed: the next change
	// opens a new one.
	if err := w.reviews.CloseReviews(ctx, w.org, runrequests.KindScheduleReview, active, user, w.now); err != nil {
		t.Fatal(err)
	}
	s := w.schedules[active]
	s.Paused, s.PausedReason = false, ""
	w.schedules[active] = s
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if sr := w.reviewOf(active); len(sr) != 2 || sr[1].Status != runrequests.StatusPending {
		t.Fatalf("after a decided review = %+v", sr)
	}
}

// A repository change pauses what runs the templates of its playbooks and
// disables the trigger that syncs it.
func TestAnAssistantsRepoChangePausesItsTemplatesAndSyncs(t *testing.T) {
	w := newReviewWorld()
	a := w.schedule("a", w.tmplA, false, "")
	b := w.schedule("b", w.tmplB, false, "")
	sync := w.trigger("push", nil, &w.repo, true)
	change := assistantchange.Change{Subject: assistantchange.SubjectRepo, ID: w.repo, Name: "infra", UserID: uuid.Must(uuid.NewV7()),
		Fields: []assistantchange.Field{{Name: "branch", Old: "main", New: "dev"}}}
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if !w.schedules[a].Paused || !w.schedules[b].Paused || w.triggers[sync].Enabled {
		t.Fatalf("not all stopped: %+v %+v %+v", w.schedules[a], w.schedules[b], w.triggers[sync])
	}
	tr := w.reviewOf(sync)
	if len(tr) != 1 || tr[0].TemplateID != uuid.Nil || tr[0].TemplateRevision != 0 || tr[0].Kind != runrequests.KindTriggerReview {
		t.Fatalf("sync trigger review = %+v", tr)
	}
	if sr := w.reviewOf(b); len(sr) != 1 || sr[0].TemplateID != w.tmplB || sr[0].TemplateRevision != 9 {
		t.Fatalf("b's review = %+v", sr)
	}
}

// A person resuming or enabling closes the item's open review.
func TestAPersonsResumeOrEnableClosesTheReview(t *testing.T) {
	w := newReviewWorld()
	sch := w.schedule("a", w.tmplA, false, "")
	hook := w.trigger("hook", &w.tmplA, nil, true)
	if err := w.reviewer.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA, Name: "deploy",
		Fields: []assistantchange.Field{{Name: "limit", Old: nil, New: "x"}}}); err != nil {
		t.Fatal(err)
	}
	person := uuid.Must(uuid.NewV7())
	if err := w.reviewer.ScheduleResumed(ctx, w.org, sch, person); err != nil {
		t.Fatal(err)
	}
	if err := w.reviewer.TriggerEnabled(ctx, w.org, hook, person); err != nil {
		t.Fatal(err)
	}
	for _, r := range w.reviews.rows {
		if r.Status != runrequests.StatusApproved || *r.DecidedBy != person {
			t.Fatalf("review = %+v", r)
		}
	}
}

// failingAudit fails every record: the change fails with it.
type failingAudit struct{}

func (failingAudit) Record(context.Context, audit.Event) error { return errors.New("audit down") }

func TestAReviewerFailureFailsTheChange(t *testing.T) {
	w := newReviewWorld()
	w.schedule("a", w.tmplA, false, "")
	r := runrequests.NewReviewer(w.reviews, runrequests.ReviewSources{Templates: w, Schedules: w, Triggers: w}, failingAudit{},
		func() time.Time { return w.now }, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
	if err := r.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA}); err == nil {
		t.Fatal("a change whose pause could not be recorded succeeded")
	}
}
