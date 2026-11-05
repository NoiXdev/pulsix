package templates_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/varsource"
)

type noResolver struct{}

func (noResolver) ResolveTemplatePlaybooks(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func seedTemplatesRepo(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := repos.NewSQLStore(d).Create(db.WithOrgScope(ctx, orgID), repos.Repo{
		ID: id, OrgID: orgID, Name: name, URL: "https://git.example.com/acme/" + name + ".git",
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestUpdateVarsSourceKeepsTheValues: a mode change or an automatic apply
// writes the fields and the source columns under the revision guard and
// leaves the stored values' bytes alone.
func TestUpdateVarsSourceKeepsTheValues(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-vars-source")
	scoped := db.WithOrgScope(ctx, org)
	store := templates.NewSQLStore(d)
	created, err := store.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: seedEditorPlaybook(t, d, org), Name: "deploy", ExtraVars: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A value float64 cannot hold: the write must not round-trip it.
	if _, err := d.Exec(scoped, `UPDATE job_templates SET extra_vars = '{"big": 9007199254740993}' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	next := created
	next.ExtraVars = map[string]any{"big": "ignored"}
	next.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "big", Type: schema.TypeInt}}}
	applied := time.Now().UTC().Truncate(time.Microsecond)
	next.VarsMode, next.VarsSourceRev, next.VarsAppliedAt = templates.ModeAutomatic, "c1", &applied
	got, err := store.UpdateVarsSource(scoped, next, revPtr(1), ptr(""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.VarsMode != templates.ModeAutomatic || got.VarsSourceRev != "c1" || !got.VarsAppliedAt.Equal(applied) ||
		got.ExtraVarsSchema == nil || len(got.ExtraVarsSchema.Fields) != 1 {
		t.Fatalf("UpdateVarsSource = %+v", got)
	}
	var raw string
	if err := d.Pool().QueryRow(ctx, `SELECT extra_vars::text FROM job_templates WHERE id = $1`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `{"big": 9007199254740993}` {
		t.Fatalf("extra_vars = %s, want untouched", raw)
	}
	if _, err := store.UpdateVarsSource(scoped, next, revPtr(1), nil); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("stale = %v, want ErrRevisionMismatch", err)
	}
	if _, err := store.UpdateVarsSource(scoped, next, revPtr(2), ptr("c0")); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("stale source rev = %v, want ErrRevisionMismatch", err)
	}

	// A new source revision without a change of fields moves neither the
	// revision nor updated_at.
	rec := got
	recorded := applied.Add(time.Minute)
	rec.VarsSourceRev, rec.VarsAppliedAt = "c2", &recorded
	if _, err := store.RecordSourceRev(scoped, rec, 2, "c0"); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("record over another source rev = %v, want ErrRevisionMismatch", err)
	}
	if _, err := store.RecordSourceRev(scoped, rec, 1, "c1"); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("record over another revision = %v, want ErrRevisionMismatch", err)
	}
	after, err := store.RecordSourceRev(scoped, rec, 2, "c1")
	if err != nil || after.Revision != 2 || !after.UpdatedAt.Equal(got.UpdatedAt) || after.VarsSourceRev != "c2" || !after.VarsAppliedAt.Equal(recorded) {
		t.Fatalf("RecordSourceRev = revision %d updated %v (was %v) rev %q, %v", after.Revision, after.UpdatedAt, got.UpdatedAt, after.VarsSourceRev, err)
	}

	// The link alone, at the revision: neither the revision nor
	// updated_at moves.
	link := after
	link.VarsMode, link.VarsSourceRev, link.VarsAppliedAt = templates.ModeManual, "", nil
	link.Revision = 1
	if _, err := store.SetVarsLink(scoped, link); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("set link at another revision = %v, want ErrRevisionMismatch", err)
	}
	link.Revision = 2
	unlinked, err := store.SetVarsLink(scoped, link)
	if err != nil || unlinked.Revision != 2 || !unlinked.UpdatedAt.Equal(got.UpdatedAt) || unlinked.VarsMode != templates.ModeManual ||
		unlinked.VarsSourceRev != "" || unlinked.VarsAppliedAt != nil || len(unlinked.ExtraVarsSchema.Fields) != 1 {
		t.Fatalf("SetVarsLink = %+v, %v", unlinked, err)
	}

	next.ID = uuid.Must(uuid.NewV7())
	if _, err := store.SetVarsLink(scoped, next); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("set link of an unknown template = %v, want ErrNotFound", err)
	}
	if _, err := store.RecordSourceRev(scoped, next, 1, ""); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("record unknown = %v, want ErrNotFound", err)
	}
	if _, err := store.UpdateVarsSource(scoped, next, nil, nil); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown = %v, want ErrNotFound", err)
	}
}

// TestApplyRepoSourcesThroughTheStores: a repository's stored read reaches
// its automatic templates (and only those) through the real playbooks and
// repos services, without a request's organization scope.
func TestApplyRepoSourcesThroughTheStores(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-auto-apply")
	other := seedTemplatesOrg(t, q, "tmpl-auto-apply-other")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedTemplatesRepo(t, d, org, "infra")
	reposSvc := repos.NewService(repos.NewSQLStore(d), nil, nil)
	playbooksSvc := playbooks.NewService(playbooks.NewSQLStore(d), reposSvc, noResolver{})
	site, err := playbooksSvc.CreateRepo(scoped, org, repoID, sitePath)
	if err != nil {
		t.Fatal(err)
	}
	store := templates.NewSQLStore(d)
	svc := templates.NewService(store, playbooksSvc, nil, nil, nil, crypto.NewBox(testBoxKey("tmpl-auto-apply-key")))

	port := asStored(candidate("port", schema.TypeInt, withDefault(float64(80))), true)
	seed := func(name string, playbookID uuid.UUID, mode templates.SourceMode) templates.Template {
		t.Helper()
		tm, err := store.Create(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: name,
			ExtraVars: map[string]any{"port": float64(8080)}, ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{port}}})
		if err != nil {
			t.Fatal(err)
		}
		tm.VarsMode, tm.VarsSourceRev = mode, "c1"
		if tm, err = store.UpdateVarsSource(scoped, tm, nil, nil); err != nil {
			t.Fatal(err)
		}
		return tm
	}
	auto := seed("auto", site.ID, templates.ModeAutomatic)
	manual := seed("manual", site.ID, templates.ModeManual)
	editor := seed("editor", seedEditorPlaybook(t, d, org), templates.ModeAutomatic)
	if err := repos.NewSQLStore(d).ReplaceVarSources(scoped, org, repoID,
		repos.SourcesRecord{Commit: "c2", SyncedAt: time.Now(), WorkerReads: true},
		[]varsource.Playbook{{Path: sitePath, Fields: []schema.Field{candidate("port", schema.TypeInt, withDefault(float64(81)))},
			Files: []varsource.FileUse{{Path: sitePath, State: varsource.FileRead}}}}); err != nil {
		t.Fatal(err)
	}

	if err := svc.ApplyRepoSources(ctx, org, repoID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyRepoSources(ctx, other, repoID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(scoped, org, auto.ID)
	if err != nil || got.ExtraVarsSchema.Fields[0].Default != float64(81) || got.VarsSourceRev != "c2" || got.Revision != auto.Revision+1 {
		t.Fatalf("automatic template = %s rev %q revision %d, %v", mustJSON(t, got.ExtraVarsSchema), got.VarsSourceRev, got.Revision, err)
	}
	if b, _ := json.Marshal(got.ExtraVars); string(b) != `{"port":8080}` {
		t.Fatalf("values = %s", b)
	}
	for _, untouched := range []templates.Template{manual, editor} {
		if got, err := store.Get(scoped, org, untouched.ID); err != nil || got.Revision != untouched.Revision {
			t.Errorf("%s written: revision %d, %v", untouched.Name, got.Revision, err)
		}
	}
}
