package schedules_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/schedules"
)

// TestPauseForReviewThroughTheStore (spec 2026-11-25 C2): an assistant's
// change pauses an active schedule with the reason assistant_change and a
// new revision; an already paused one is left as it is (ok false); the
// schedules of a set of templates are listed; resuming clears the reason.
func TestPauseForReviewThroughTheStore(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-review")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	next := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	in := schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "Nightly", Mode: schedules.ModeCron,
		Cron: "0 3 * * *", Timezone: "UTC", NextRunAt: &next,
	}
	created, err := st.Create(scoped, in)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListOfTemplates(scoped, org, []uuid.UUID{templateID, uuid.Must(uuid.NewV7())})
	if err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	paused, ok, err := st.PauseForReview(scoped, org, created.ID)
	if err != nil || !ok || !paused.Paused || paused.PausedReason != schedules.PausedForReview || paused.NextRunAt != nil || paused.Revision != created.Revision+1 {
		t.Fatalf("paused = %+v ok %v, %v", paused, ok, err)
	}
	again, ok, err := st.PauseForReview(scoped, org, created.ID)
	if err != nil || ok || again.Revision != paused.Revision || again.PausedReason != schedules.PausedForReview {
		t.Fatalf("again = %+v ok %v, %v", again, ok, err)
	}
	if _, _, err := st.PauseForReview(scoped, org, uuid.Must(uuid.NewV7())); !errors.Is(err, schedules.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	// A schedule a person paused gets the reason too (fix round 1), and
	// an approved review of it clears the reason at the revision seen and
	// leaves it paused.
	other := in
	other.ID, other.Name = uuid.Must(uuid.NewV7()), "Weekly"
	byPerson, err := st.Create(scoped, other)
	if err != nil {
		t.Fatal(err)
	}
	if byPerson, err = st.SetPaused(scoped, org, byPerson.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	marked, ok, err := st.PauseForReview(scoped, org, byPerson.ID)
	if err != nil || !ok || !marked.Paused || marked.PausedReason != schedules.PausedForReview || marked.Revision != byPerson.Revision+1 {
		t.Fatalf("person-paused = %+v ok %v, %v", marked, ok, err)
	}
	if _, err := st.ClearReasonAt(scoped, org, byPerson.ID, marked.Revision-1); !errors.Is(err, schedules.ErrRevisionMismatch) {
		t.Fatalf("stale clear = %v", err)
	}
	cleared, err := st.ClearReasonAt(scoped, org, byPerson.ID, marked.Revision)
	if err != nil || !cleared.Paused || cleared.PausedReason != "" || cleared.Revision != marked.Revision+1 {
		t.Fatalf("cleared = %+v, %v", cleared, err)
	}
	// Pausing a paused schedule keeps its reason; resuming clears it.
	if p, err := st.SetPaused(scoped, org, created.ID, true, nil); err != nil || p.PausedReason != schedules.PausedForReview {
		t.Fatalf("pause = %+v, %v", p, err)
	}
	resumed, err := st.SetPaused(scoped, org, created.ID, false, &next)
	if err != nil || resumed.Paused || resumed.PausedReason != "" {
		t.Fatalf("resumed = %+v, %v", resumed, err)
	}
}

// fakeReviews records the reviews a person's resume closes.
type fakeReviews struct {
	closed []uuid.UUID
	by     uuid.UUID
	err    error
}

func (f *fakeReviews) ScheduleResumed(_ context.Context, _, id, by uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.closed, f.by = append(f.closed, id), by
	return nil
}

// TestAPersonsResumeClosesTheReview: a person resumes a schedule an
// assistant's change paused directly (they are a person); its review is
// closed as decided by them. Resuming a schedule a person paused closes
// nothing.
func TestAPersonsResumeClosesTheReview(t *testing.T) {
	w := newWorld(t)
	reviews := &fakeReviews{}
	w.svc.SetReviews(reviews)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := w.svc.PauseForReview(ctx, w.org, s.ID); err != nil || !ok {
		t.Fatalf("pause for review = %v %v", ok, err)
	}
	person := uuid.Must(uuid.NewV7())
	r, err := w.svc.Resume(ctx, w.org, s.ID, person)
	if err != nil || r.Paused || r.PausedReason != "" {
		t.Fatalf("resumed = %+v, %v", r, err)
	}
	if len(reviews.closed) != 1 || reviews.closed[0] != s.ID || reviews.by != person {
		t.Fatalf("closed = %+v by %v", reviews.closed, reviews.by)
	}
	if _, err := w.svc.Pause(ctx, w.org, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Resume(ctx, w.org, s.ID, person); err != nil {
		t.Fatal(err)
	}
	if len(reviews.closed) != 1 {
		t.Fatalf("a person's pause closed a review: %+v", reviews.closed)
	}
	// Closing fails: the resume fails with it.
	if _, _, err := w.svc.PauseForReview(ctx, w.org, s.ID); err != nil {
		t.Fatal(err)
	}
	reviews.err = errors.New("boom")
	if _, err := w.svc.Resume(ctx, w.org, s.ID, person); err == nil {
		t.Fatal("a resume whose review could not be closed succeeded")
	}
}
