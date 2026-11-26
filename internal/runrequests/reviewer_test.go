package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// memReviews is the review part of the store.
var ctx = context.Background()

type memReviews struct {
	rows []runrequests.Request
	// refreshMiss makes the next RefreshReview find no pending row (a
	// person decided it in between).
	refreshMiss bool
}

func (m *memReviews) LatestReview(_ context.Context, _ uuid.UUID, kind runrequests.Kind, target uuid.UUID) (runrequests.Request, error) {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if r := m.rows[i]; r.Kind == kind && *r.TargetID == target {
			return r, nil
		}
	}
	return runrequests.Request{}, runrequests.ErrNotFound
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
	if m.refreshMiss {
		m.refreshMiss = false
		return runrequests.ErrNotFound
	}
	for i, r := range m.rows {
		if r.ID == id {
			c := change
			m.rows[i].Review, m.rows[i].ExpiresAt, m.rows[i].TemplateRevision = &c, expiresAt, rev
		}
	}
	return nil
}

func (m *memReviews) CloseReviews(_ context.Context, _ uuid.UUID, kind runrequests.Kind, target uuid.UUID, to runrequests.Status, by uuid.UUID, at time.Time) error {
	for i, r := range m.rows {
		if r.Kind == kind && *r.TargetID == target && r.Status == runrequests.StatusPending {
			m.rows[i].Status, m.rows[i].DecidedBy, m.rows[i].DecidedAt = to, &by, &at
		}
	}
	return nil
}

type reviewWorld struct {
	org, tmplA, tmplB, repo uuid.UUID
	playbook                uuid.UUID
	schedules               map[uuid.UUID]schedules.Schedule
	triggers                map[uuid.UUID]triggers.Trigger
	events                  []audit.Event
	reviews                 *memReviews
	reviewer                *runrequests.Reviewer
	now                     time.Time
	// pausedBy answers who paused or disabled an item before; asked
	// records the questions.
	pausedBy *audit.Stopper
	asked    []string
	// locked overrides what LockForReview reads (the row as committed
	// when it differs from the list); reaching maps a template to the
	// templates that start it.
	locked   map[uuid.UUID]schedules.Schedule
	reaching map[uuid.UUID][]uuid.UUID
	// overlap is the hosts an inventory change reaches in each template's
	// run, limits each template's limit; changed records what the reach
	// was asked for.
	overlap map[uuid.UUID][]string
	limits  map[uuid.UUID]string
	changed []render.Scope
}

func (w *reviewWorld) LockForReview(_ context.Context, _, id uuid.UUID) (schedules.Schedule, error) {
	w.asked = append(w.asked, "lock "+id.String())
	if s, ok := w.locked[id]; ok {
		w.schedules[id] = s
		return s, nil
	}
	return w.schedules[id], nil
}

// worldTriggers is the trigger side of the world.
type worldTriggers struct{ *reviewWorld }

func (w worldTriggers) LockForReview(_ context.Context, _, id uuid.UUID) (triggers.Trigger, error) {
	return w.triggers[id], nil
}

func (w *reviewWorld) IDsReaching(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	out := append([]uuid.UUID(nil), ids...)
	for _, id := range ids {
		out = append(out, w.reaching[id]...)
	}
	return out, nil
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

func (w *reviewWorld) IDsOfPlaybook(_ context.Context, _, playbook uuid.UUID) ([]uuid.UUID, error) {
	if playbook == w.playbook {
		return []uuid.UUID{w.tmplA}, nil
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
	if s.PausedReason != "" {
		return s, false, nil
	}
	s.Paused, s.PausedReason, s.Revision = true, schedules.PausedForReview, s.Revision+1
	w.schedules[id] = s
	return s, true, nil
}

func (w worldTriggers) ListForReview(_ context.Context, _ uuid.UUID, ids []uuid.UUID, repo *uuid.UUID) ([]triggers.Trigger, error) {
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

func (w worldTriggers) DisableForReview(_ context.Context, _, id uuid.UUID) (triggers.Trigger, bool, error) {
	t := w.triggers[id]
	if t.DisabledReason != "" {
		return t, false, nil
	}
	t.Enabled, t.DisabledReason, t.Revision = false, triggers.DisabledForReview, t.Revision+1
	w.triggers[id] = t
	return t, true, nil
}

func (w *reviewWorld) LastActor(_ context.Context, _ uuid.UUID, objectType, objectID string, _ time.Time, actions audit.StopActions) (*audit.Stopper, error) {
	w.asked = append(w.asked, objectType+" "+objectID+" "+strings.Join(actions.Stop, ","))
	return w.pausedBy, nil
}

func (w *reviewWorld) Record(_ context.Context, e audit.Event) error {
	w.events = append(w.events, e)
	return nil
}

func newReviewWorld() *reviewWorld {
	w := &reviewWorld{
		org: uuid.Must(uuid.NewV7()), tmplA: uuid.Must(uuid.NewV7()), tmplB: uuid.Must(uuid.NewV7()), repo: uuid.Must(uuid.NewV7()),
		playbook:  uuid.Must(uuid.NewV7()),
		schedules: map[uuid.UUID]schedules.Schedule{}, triggers: map[uuid.UUID]triggers.Trigger{},
		reviews: &memReviews{}, now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
	}
	w.reviewer = runrequests.NewReviewer(w.reviews, runrequests.ReviewSources{Templates: w, Schedules: w, Triggers: worldTriggers{w}, History: w, Reach: w}, w,
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
// the token, and an activity entry. The items of another template are left
// alone.
func TestAnAssistantsTemplateChangePausesWhatRunsIt(t *testing.T) {
	w := newReviewWorld()
	active := w.schedule("nightly", w.tmplA, false, "")
	other := w.schedule("other", w.tmplB, false, "")
	hook := w.trigger("deploy-hook", &w.tmplA, nil, true)
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
	if w.schedules[other].Paused {
		t.Fatalf("another template's schedule = %+v", w.schedules[other])
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
	if tr := w.reviewOf(hook); len(tr) != 1 || tr[0].Kind != runrequests.KindTriggerReview || tr[0].Review.PausedBefore != nil {
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
	if err := w.reviews.CloseReviews(ctx, w.org, runrequests.KindScheduleReview, active, runrequests.StatusApproved, user, w.now); err != nil {
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
	r := runrequests.NewReviewer(w.reviews, runrequests.ReviewSources{Templates: w, Schedules: w, Triggers: worldTriggers{w}, History: w, Reach: w}, failingAudit{},
		func() time.Time { return w.now }, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
	if err := r.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA}); err == nil {
		t.Fatal("a change whose pause could not be recorded succeeded")
	}
}

// Fix round 1: a schedule a person paused, and a trigger a person disabled,
// get the reason and a review too, which remembers who had stopped them, so
// the notice shows before anyone resumes them. The record of who did it is
// asked by the item's pause or disable actions.
func TestAnAssistantsChangeMarksWhatAPersonStopped(t *testing.T) {
	w := newReviewWorld()
	person := uuid.Must(uuid.NewV7())
	w.pausedBy = &audit.Stopper{UserID: &person}
	byPerson := w.schedule("weekly", w.tmplA, true, "")
	off := w.trigger("old-hook", &w.tmplA, nil, false)
	change := assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA, Name: "deploy",
		Fields: []assistantchange.Field{{Name: "limit", Old: nil, New: "db*"}}}
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if s := w.schedules[byPerson]; !s.Paused || s.PausedReason != schedules.PausedForReview {
		t.Fatalf("schedule = %+v", s)
	}
	if tr := w.triggers[off]; tr.Enabled || tr.DisabledReason != triggers.DisabledForReview {
		t.Fatalf("trigger = %+v", tr)
	}
	for _, id := range []uuid.UUID{byPerson, off} {
		r := w.reviewOf(id)
		if len(r) != 1 || r[0].Review.PausedBefore == nil || r[0].Review.PausedBefore.By == nil || *r[0].Review.PausedBefore.By != person {
			t.Fatalf("review of %s = %+v", id, r)
		}
	}
	if !slices.Contains(w.asked, "schedule "+byPerson.String()+" POST pauseSchedule") ||
		!slices.ContainsFunc(w.asked, func(a string) bool { return strings.HasPrefix(a, "trigger "+off.String()+" POST disableTrigger") }) {
		t.Fatalf("asked = %v", w.asked)
	}
	if len(w.events) != 2 || !strings.Contains(w.events[0].Summary, "kept schedule weekly paused") {
		t.Fatalf("events = %+v", w.events)
	}
	// Another change refreshes the review and keeps who had paused it.
	w.pausedBy = nil
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if r := w.reviewOf(byPerson); len(r) != 1 || len(r[0].Review.Changes) != 2 || r[0].Review.PausedBefore == nil || *r[0].Review.PausedBefore.By != person {
		t.Fatalf("refreshed = %+v", r)
	}
}

// An inline playbook's change reaches the templates that run it.
func TestAnAssistantsPlaybookChangePausesItsTemplates(t *testing.T) {
	w := newReviewWorld()
	a := w.schedule("a", w.tmplA, false, "")
	b := w.schedule("b", w.tmplB, false, "")
	if err := w.reviewer.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectPlaybook, ID: w.playbook, Name: "deploy",
		Fields: []assistantchange.Field{{Name: "playbook_text", Old: "- hosts: all", New: "- hosts: web"}}}); err != nil {
		t.Fatal(err)
	}
	if !w.schedules[a].Paused || w.schedules[b].Paused {
		t.Fatalf("schedules = %+v %+v", w.schedules[a], w.schedules[b])
	}
}

// Final review 1: a change of a template reaches the schedules of every
// template that starts it as a follow-up.
func TestAChangeReachesTheTemplatesThatStartIt(t *testing.T) {
	w := newReviewWorld()
	w.reaching = map[uuid.UUID][]uuid.UUID{w.tmplB: {w.tmplA}}
	a := w.schedule("a", w.tmplA, false, "")
	if err := w.reviewer.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplB, Name: "cleanup",
		Fields: []assistantchange.Field{{Name: "limit", Old: nil, New: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if s := w.schedules[a]; !s.Paused || s.PausedReason != schedules.PausedForReview {
		t.Fatalf("A's schedule = %+v", s)
	}
	if r := w.reviewOf(a); len(r) != 1 || r[0].TemplateID != w.tmplA {
		t.Fatalf("review = %+v", r)
	}
}

// Final review 2: a schedule a person paused keeps that fact across a
// rejected review: the next change's review carries it over, so its
// approval still only clears the reason.
func TestPausedBeforeCarriesOverAfterARejection(t *testing.T) {
	w := newReviewWorld()
	person := uuid.Must(uuid.NewV7())
	w.pausedBy = &audit.Stopper{UserID: &person}
	s := w.schedule("weekly", w.tmplA, true, "")
	change := assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA, Name: "deploy",
		Fields: []assistantchange.Field{{Name: "limit", Old: nil, New: "x"}}}
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	w.reviews.rows[0].Status = runrequests.StatusRejected
	w.pausedBy = nil
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	r := w.reviewOf(s)
	if len(r) != 2 || r[1].Review.PausedBefore == nil || r[1].Review.PausedBefore.By == nil || *r[1].Review.PausedBefore.By != person {
		t.Fatalf("reviews = %+v", r)
	}
}

// Final review 3: the decision is made on the locked row, not the listed
// one; and a refresh that finds no pending review (a person decided it in
// between) opens a new review.
func TestTheReviewerDecidesOnTheLockedRow(t *testing.T) {
	w := newReviewWorld()
	s := w.schedule("nightly", w.tmplA, true, schedules.PausedForReview)
	// Listed as paused for review; committed meanwhile as resumed.
	w.locked = map[uuid.UUID]schedules.Schedule{s: {ID: s, OrgID: w.org, TemplateID: w.tmplA, Name: "nightly", Revision: 3}}
	change := assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA, Name: "deploy",
		Fields: []assistantchange.Field{{Name: "limit", Old: nil, New: "x"}}}
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if got := w.schedules[s]; !got.Paused || got.PausedReason != schedules.PausedForReview {
		t.Fatalf("schedule = %+v", got)
	}
	if r := w.reviewOf(s); len(r) != 1 || r[0].Review.PausedBefore != nil || len(w.events) != 1 {
		t.Fatalf("reviews = %+v, events %d", r, len(w.events))
	}
	w.locked = nil
	w.reviews.refreshMiss = true
	if err := w.reviewer.Changed(ctx, w.org, change); err != nil {
		t.Fatal(err)
	}
	if r := w.reviewOf(s); len(r) != 2 || r[1].Status != runrequests.StatusPending || len(r[1].Review.Changes) != 1 {
		t.Fatalf("after a missed refresh = %+v", r)
	}
}

// Final review 6: a stop made with an assistant token names the token.
func TestAnAssistantsPauseIsNamedAsTheAssistant(t *testing.T) {
	w := newReviewWorld()
	w.pausedBy = &audit.Stopper{UserID: ptrTo(uuid.Must(uuid.NewV7())), TokenName: "claude-ops", Assistant: true}
	s := w.schedule("weekly", w.tmplA, true, "")
	if err := w.reviewer.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA}); err != nil {
		t.Fatal(err)
	}
	if r := w.reviewOf(s); len(r) != 1 || r[0].Review.PausedBefore == nil || r[0].Review.PausedBefore.AssistantToken != "claude-ops" {
		t.Fatalf("review = %+v", r[0].Review.PausedBefore)
	}
}

// Final review 4: a deleted item's review is closed as rejected.
func TestADeletedItemsReviewIsClosed(t *testing.T) {
	w := newReviewWorld()
	s := w.schedule("a", w.tmplA, false, "")
	if err := w.reviewer.Changed(ctx, w.org, assistantchange.Change{Subject: assistantchange.SubjectTemplate, ID: w.tmplA}); err != nil {
		t.Fatal(err)
	}
	person := uuid.Must(uuid.NewV7())
	if err := w.reviewer.ScheduleDeleted(ctx, w.org, s, person); err != nil {
		t.Fatal(err)
	}
	if r := w.reviewOf(s); r[0].Status != runrequests.StatusRejected || *r[0].DecidedBy != person {
		t.Fatalf("review = %+v", r[0])
	}
}

func ptrTo(id uuid.UUID) *uuid.UUID { return &id }
