package schedules_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

// seedTemplate creates an organization with one editor playbook and one
// job template through the raw pool (fixtures bypass RLS).
func seedTemplate(t *testing.T, d *db.DB, slug string) (org, templateID uuid.UUID) {
	t.Helper()
	q := gen.New(d.Pool())
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	inline := "- hosts: all\n  tasks: []\n"
	pb, err := q.CreatePlaybook(ctx, gen.CreatePlaybookParams{ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Inline: &inline, Source: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := q.InsertTemplate(ctx, gen.InsertTemplateParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Name: "deploy", PlaybookID: db.NullUUID(&pb.ID),
		Scope: []byte(`{}`), ExtraVars: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID, tmpl.ID
}

func TestStoreRoundTripAndConstraints(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-store")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	next := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	limit := "web*"
	in := schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "Nightly", Mode: schedules.ModeSimple,
		Cron: "0 3 * * 2,4", Simple: &cronexpr.Simple{Kind: "weekly", Days: []string{"tue", "thu"}, Time: "03:00"},
		Timezone: "Europe/Berlin", Guards: schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}},
		Overrides: templates.Overrides{Limit: &limit, ExtraVars: map[string]any{"a": "b"}}, NextRunAt: &next,
	}
	got, err := st.Create(scoped, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || got.Simple.Days[1] != "thu" || *got.Overrides.Limit != "web*" || !got.NextRunAt.Equal(next) {
		t.Fatalf("round trip = %+v", got)
	}
	if _, err := st.Create(scoped, withID(in)); !errors.Is(err, schedules.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	in2 := withID(in)
	in2.Name = "Other"
	in2.TemplateID = uuid.Must(uuid.NewV7())
	if _, err := st.Create(scoped, in2); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("unknown template: %v", err)
	}
	got.Cron = "*/5 * * * *"
	if _, err := st.Update(scoped, got, got.Revision+1); !errors.Is(err, schedules.ErrRevisionMismatch) {
		t.Fatalf("stale: %v", err)
	}
	up, err := st.Update(scoped, got, got.Revision)
	if err != nil || up.Revision != 2 || up.Cron != "*/5 * * * *" {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if n, err := st.PauseAll(scoped, org); err != nil || n != 1 {
		t.Fatalf("PauseAll = %d, %v", n, err)
	}
	group := uuid.Must(uuid.NewV7())
	if err := st.RecordRun(scoped, schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: org, ScheduleID: got.ID, DueAt: next, DecidedAt: next, Outcome: schedules.OutcomeTriggered, JobGroupID: &group}); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LatestRuns(scoped, org)
	if err != nil || *latest[got.ID].JobGroupID != group {
		t.Fatalf("latest = %v, %v", latest, err)
	}
	paused, err := st.SetPaused(scoped, org, got.ID, false, &next)
	if err != nil || paused.Paused || !paused.NextRunAt.Equal(next) {
		t.Fatalf("SetPaused = %+v, %v", paused, err)
	}
	if _, err := st.SetPaused(scoped, org, uuid.Must(uuid.NewV7()), true, nil); !errors.Is(err, schedules.ErrNotFound) {
		t.Fatalf("SetPaused unknown: %v", err)
	}
	runs, err := st.Runs(scoped, org, got.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Outcome != schedules.OutcomeTriggered {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	if err := st.Delete(scoped, org, uuid.Must(uuid.NewV7())); !errors.Is(err, schedules.ErrNotFound) {
		t.Fatalf("Delete unknown: %v", err)
	}
	// Deleting the template cascades to schedules and runs.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM job_templates WHERE id = $1`, templateID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, got.ID); !errors.Is(err, schedules.ErrNotFound) {
		t.Fatalf("after template delete: %v", err)
	}
}

func withID(s schedules.Schedule) schedules.Schedule {
	s.ID = uuid.Must(uuid.NewV7())
	return s
}
