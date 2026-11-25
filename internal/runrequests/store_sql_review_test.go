package runrequests_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/runrequests"
)

func (f sqlFixture) review(target uuid.UUID, token *uuid.UUID, now time.Time) runrequests.Request {
	return runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindScheduleReview, TemplateID: f.template,
		Reason: "an assistant changed job template deploy", RequestedByUser: f.user, RequestedByToken: token,
		Status: runrequests.StatusPending, ExpiresAt: now.Add(runrequests.ReviewTTL), CreatedAt: now, TargetID: &target,
		Review: &runrequests.ReviewChange{Changes: []assistantchange.Change{{
			Subject: assistantchange.SubjectTemplate, ID: f.template, Name: "deploy", UserID: f.user, TokenID: token,
			Fields: []assistantchange.Field{{Name: "limit", Old: "web*", New: "db*"}},
		}}},
	}
}

// TestSQLStoreReviews (spec 2026-11-25 C2): a review round-trips with its
// changes; the open one of an item is found by its target while pending
// and unexpired, refreshed in place, closed as decided by a person, and
// the newest of each item is listed. A review counts on the badge but not
// against its token's cap.
func TestSQLStoreReviews(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-review")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	target, token := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	r := f.review(target, &token, now)
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(f.ctx, f.org, r.ID)
	if err != nil || got.Review == nil || len(got.Review.Changes) != 1 || got.Review.Changes[0].Fields[0].New != "db*" || got.Schedule != nil {
		t.Fatalf("Get = %+v %v", got, err)
	}
	open, err := store.OpenReview(f.ctx, f.org, runrequests.KindScheduleReview, target, now)
	if err != nil || open.ID != r.ID {
		t.Fatalf("open = %+v %v", open, err)
	}
	if _, err := store.OpenReview(f.ctx, f.org, runrequests.KindTriggerReview, target, now); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("other kind = %v", err)
	}
	if _, err := store.OpenReview(f.ctx, f.org, runrequests.KindScheduleReview, target, now.Add(runrequests.ReviewTTL)); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("expired = %v", err)
	}
	if n, err := store.CountPending(f.ctx, f.org, nil, now); err != nil || n != 1 {
		t.Fatalf("badge = %d %v", n, err)
	}
	if n, err := store.CountPending(f.ctx, f.org, &token, now); err != nil || n != 0 {
		t.Fatalf("token's own = %d %v", n, err)
	}
	more := *got.Review
	more.Changes = append(more.Changes, more.Changes[0])
	later := now.Add(time.Hour)
	if err := store.RefreshReview(f.ctx, f.org, r.ID, more, later.Add(runrequests.ReviewTTL), 7); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(f.ctx, f.org, r.ID)
	if err != nil || len(got.Review.Changes) != 2 || !got.ExpiresAt.Equal(later.Add(runrequests.ReviewTTL)) || got.TemplateRevision != 7 {
		t.Fatalf("refreshed = %+v %v", got, err)
	}
	latest, err := store.LatestReviews(f.ctx, f.org, runrequests.KindScheduleReview, []uuid.UUID{target, uuid.Must(uuid.NewV7())})
	if err != nil || len(latest) != 1 || latest[target] != r.ID {
		t.Fatalf("latest = %+v %v", latest, err)
	}
	if err := store.CloseReviews(f.ctx, f.org, runrequests.KindScheduleReview, target, runrequests.StatusApproved, f.user, later); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(f.ctx, f.org, r.ID)
	if err != nil || got.Status != runrequests.StatusApproved || got.DecidedBy == nil || *got.DecidedBy != f.user || got.RunGroupID != nil {
		t.Fatalf("closed = %+v %v", got, err)
	}
	if _, err := store.OpenReview(f.ctx, f.org, runrequests.KindScheduleReview, target, later); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("after close = %v", err)
	}
	// A refresh of a review decided in between finds nothing to refresh
	// (final review 3); the newest review is found whatever its state.
	if err := store.RefreshReview(f.ctx, f.org, r.ID, more, later.Add(runrequests.ReviewTTL), 8); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("refresh of a decided review = %v", err)
	}
	if latest, err := store.LatestReview(f.ctx, f.org, runrequests.KindScheduleReview, target); err != nil || latest.ID != r.ID {
		t.Fatalf("latest = %+v %v", latest, err)
	}
	if _, err := store.LatestReview(f.ctx, f.org, runrequests.KindTriggerReview, target); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("no latest = %v", err)
	}
	// The open change reviews of the template: by the item's template and
	// by the change's subject.
	r2 := f.review(uuid.Must(uuid.NewV7()), &token, later)
	if err := store.Insert(f.ctx, r2); err != nil {
		t.Fatal(err)
	}
	if open, err := store.OpenChangeReviews(f.ctx, f.org, &f.template, nil); err != nil || len(open) != 1 || open[0] != r2.ID {
		t.Fatalf("open change reviews = %v %v", open, err)
	}
	other := uuid.Must(uuid.NewV7())
	if open, err := store.OpenChangeReviews(f.ctx, f.org, &other, &other); err != nil || len(open) != 0 {
		t.Fatalf("none = %v %v", open, err)
	}
	// Closing again, or without a person (uuid.Nil), is harmless.
	if err := store.CloseReviews(f.ctx, f.org, runrequests.KindScheduleReview, target, runrequests.StatusApproved, uuid.Nil, later); err != nil {
		t.Fatal(err)
	}
}
