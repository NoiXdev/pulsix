package triggers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// TestDisableForReviewThroughTheStore (spec 2026-11-25 C2): an assistant's
// change disables an enabled trigger with the reason assistant_change; a
// disabled one is left alone (ok false); the triggers that run a set of
// templates or sync a repository are listed; enabling at the revision the
// person saw clears the reason, and so does a person's update that
// enables it.
func TestDisableForReviewThroughTheStore(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-review")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	repo, err := gen.New(d.Pool()).CreateRepo(ctx, gen.CreateRepoParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", Url: "https://git.example.com/infra.git", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.Create(scoped, storedTrigger(org, tmpl, triggers.DefaultRateLimit()), []byte("sealed"))
	if err != nil {
		t.Fatal(err)
	}
	sync := triggers.Trigger{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: triggers.KindRepoSync, RepoID: &repo.ID,
		Name: "sync", Enabled: true, Preset: triggers.PresetGitHub, OnActive: triggers.OnActiveSkip, RateLimit: triggers.DefaultRateLimit()}
	if _, err := st.Create(scoped, sync, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListForReview(scoped, org, []uuid.UUID{tmpl}, nil)
	if err != nil || len(list) != 1 || list[0].ID != run.ID {
		t.Fatalf("by template = %+v, %v", list, err)
	}
	list, err = st.ListForReview(scoped, org, nil, &repo.ID)
	if err != nil || len(list) != 1 || list[0].ID != sync.ID {
		t.Fatalf("by repo = %+v, %v", list, err)
	}
	if list, err = st.ListForReview(scoped, org, []uuid.UUID{tmpl}, &repo.ID); err != nil || len(list) != 2 {
		t.Fatalf("both = %+v, %v", list, err)
	}
	off, ok, err := st.DisableForReview(scoped, org, run.ID)
	if err != nil || !ok || off.Enabled || off.DisabledReason != triggers.DisabledForReview || off.Revision != run.Revision+1 {
		t.Fatalf("disabled = %+v ok %v, %v", off, ok, err)
	}
	if again, ok, err := st.DisableForReview(scoped, org, run.ID); err != nil || ok || again.Revision != off.Revision {
		t.Fatalf("again = %+v ok %v, %v", again, ok, err)
	}
	if _, _, err := st.DisableForReview(scoped, org, uuid.Must(uuid.NewV7())); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	if _, err := st.EnableAt(scoped, org, run.ID, off.Revision-1); !errors.Is(err, triggers.ErrRevisionMismatch) {
		t.Fatalf("stale = %v", err)
	}
	on, err := st.EnableAt(scoped, org, run.ID, off.Revision)
	if err != nil || !on.Enabled || on.DisabledReason != "" || on.Revision != off.Revision+1 {
		t.Fatalf("enabled = %+v, %v", on, err)
	}
	// A person's update that enables a trigger disabled for review clears
	// the reason; one that keeps it disabled keeps it.
	off, _, err = st.DisableForReview(scoped, org, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	renamed := off
	renamed.Name = "renamed"
	kept, err := st.Update(scoped, renamed, off.Revision)
	if err != nil || kept.DisabledReason != triggers.DisabledForReview {
		t.Fatalf("kept = %+v, %v", kept, err)
	}
	kept.Enabled = true
	if up, err := st.Update(scoped, kept, kept.Revision); err != nil || up.DisabledReason != "" {
		t.Fatalf("enabled by an update = %+v, %v", up, err)
	}
}

type fakeReviews struct {
	closed []uuid.UUID
	by     uuid.UUID
}

func (f *fakeReviews) TriggerEnabled(_ context.Context, _, id, by uuid.UUID) error {
	f.closed, f.by = append(f.closed, id), by
	return nil
}

// TestAPersonsEnableClosesTheReview: a person's update that enables a
// trigger an assistant's change disabled closes its review as decided by
// them; an update that leaves it disabled closes nothing.
func TestAPersonsEnableClosesTheReview(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-review-svc")
	scoped := db.WithOrgScope(ctx, org)
	svc := triggers.NewService(triggers.Deps{Store: triggers.NewSQLStore(d), Templates: fakeTemplates{byID: map[uuid.UUID]templates.Template{tmpl: {ID: tmpl, OrgID: org, Name: "deploy"}}}, Box: fakeBox{}})
	reviews := &fakeReviews{}
	svc.SetReviews(reviews)
	in := triggers.Input{Kind: triggers.KindTemplateRun, TemplateID: &tmpl, Name: "deploy-hook", Enabled: true, Preset: triggers.PresetPulsix}
	c, err := svc.Create(scoped, org, uuid.Must(uuid.NewV7()), in)
	if err != nil {
		t.Fatal(err)
	}
	off, _, err := svc.DisableForReview(scoped, org, c.Trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	person := uuid.Must(uuid.NewV7())
	in.Enabled, in.Name = false, "renamed"
	kept, err := svc.UpdateBy(scoped, org, c.Trigger.ID, person, off.Revision, in)
	if err != nil || len(reviews.closed) != 0 || kept.DisabledReason != triggers.DisabledForReview {
		t.Fatalf("kept = %+v closed %v, %v", kept, reviews.closed, err)
	}
	in.Enabled = true
	on, err := svc.UpdateBy(scoped, org, c.Trigger.ID, person, kept.Revision, in)
	if err != nil || !on.Enabled || on.DisabledReason != "" || len(reviews.closed) != 1 || reviews.closed[0] != c.Trigger.ID || reviews.by != person {
		t.Fatalf("enabled = %+v closed %v by %v, %v", on, reviews.closed, reviews.by, err)
	}
}
