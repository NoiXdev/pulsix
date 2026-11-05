// A template's variables linked to its playbook's sources (spec 7.75 goal
// 5): linking goes through a preview, a person's apply recomputes it and
// re-enters what no longer fits, and unlinking turns every field manual.
package templates_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/varsource"
)

const sitePath = "site.yml"

// candidate is a field read from an inline marker in site.yml.
func candidate(name string, typ schema.Type, mods ...func(*schema.Field)) schema.Field {
	f := schema.Field{Name: name, Type: typ, Origin: schema.OriginSource,
		Source: &schema.FieldSource{Kind: schema.SourceMarker, File: sitePath, Line: 1}}
	for _, m := range mods {
		m(&f)
	}
	return f
}

func required(f *schema.Field) { f.Required = true }

func setInPlaybook(f *schema.Field) { f.SetInPlaybook = true }

func withDefault(v any) func(*schema.Field) { return func(f *schema.Field) { f.Default = v } }

// asStored is c as an earlier apply stored it, with its active switch.
func asStored(c schema.Field, active bool) schema.Field {
	c.Active = &active
	return c
}

// syncedAt is when readSource's repository sync recorded its read.
var syncedAt = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

// readSource is a completely read source of site.yml at rev, from a repo
// sync by a worker that reads sources.
func readSource(repo uuid.UUID, rev string, fields ...schema.Field) playbooks.VarSource {
	at := syncedAt
	return playbooks.VarSource{
		Kind: playbooks.VarSourceRepo, Rev: rev, SyncedAt: &at, WorkerReads: true, Found: true,
		RepoID: &repo, Path: sitePath,
		Playbook: varsource.Playbook{
			Path: sitePath, Fields: fields,
			Files: []varsource.FileUse{{Path: sitePath, State: varsource.FileRead}},
			Notes: []varsource.Note{},
		},
	}
}

// linked stores a template of f.playbook with fields and values, then
// marks it linked in mode at rev, as an earlier apply would have.
func (f fixture) linked(t *testing.T, name string, mode templates.SourceMode, rev string, fields []schema.Field, values map[string]any) templates.Template {
	t.Helper()
	return f.linkedTo(t, f.playbook, name, mode, rev, fields, values)
}

// linkedTo is linked for a template of playbook.
func (f fixture) linkedTo(t *testing.T, playbook uuid.UUID, name string, mode templates.SourceMode, rev string, fields []schema.Field, values map[string]any) templates.Template {
	t.Helper()
	created, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name: name, PlaybookID: playbook, ExtraVarsSchema: &schema.Schema{Fields: fields}, ExtraVars: values,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	stored := f.store.byID[created.ID]
	stored.VarsMode, stored.VarsSourceRev = mode, rev
	f.store.byID[created.ID] = stored
	return stored
}

func fieldNames(fields []schema.Field) []string {
	out := make([]string, len(fields))
	for i, fl := range fields {
		out[i] = fl.Name
	}
	return out
}

// valueProblems are the problem paths of err, which must be a
// *schema.ValidationError.
func valueProblems(t *testing.T, err error) []string {
	t.Helper()
	var ve *schema.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *schema.ValidationError", err)
	}
	return problemPaths(err)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestLinkThroughPreview: an unlinked template is linked only by applying
// a preview, which offers every candidate as added with its default
// activation.
func TestLinkThroughPreview(t *testing.T) {
	f := newFixture()
	repo := uuid.Must(uuid.NewV7())
	f.sources[f.playbook] = readSource(repo, "c1",
		candidate("release", schema.TypeString, required),
		candidate("nginx_port", schema.TypeInt, withDefault(float64(80))),
		candidate("app_env", schema.TypeString, required, setInPlaybook),
	)
	tm, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeManual, nil); !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("mode manual on an unlinked template: %v, want ErrInvalidInput", err)
	}

	p, info, err := f.svc.PreviewVariables(ctx, f.org, tm.ID)
	if err != nil {
		t.Fatal(err)
	}
	var added []string
	for _, a := range p.Added {
		added = append(added, a.Field.Name)
	}
	if !reflect.DeepEqual(added, []string{"release", "nginx_port", "app_env"}) || p.SourceRev != "c1" {
		t.Fatalf("preview added %v rev %q", added, p.SourceRev)
	}
	if info.Rev != "c1" || !info.Found || !info.Available || !info.Newer || info.AppliedRev != "" || info.Path != sitePath {
		t.Fatalf("source = %+v", info)
	}

	// The required field is active and needs a value.
	_, err = f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &syncedAt})
	if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"release"}) {
		t.Fatalf("apply without release: problems %v", got)
	}
	applied, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{
		Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &syncedAt, Values: map[string]any{"release": "1.0"}, IfRevision: rev(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := f.store.byID[tm.ID]
	fields := stored.ExtraVarsSchema.Fields
	if !reflect.DeepEqual(fieldNames(fields), []string{"release", "nginx_port", "app_env"}) {
		t.Fatalf("fields = %v", fieldNames(fields))
	}
	for i, want := range []bool{true, false, false} {
		if fields[i].Origin != schema.OriginSource || fields[i].IsActive() != want || fields[i].Source == nil {
			t.Errorf("field %s = %s, want origin source, active %v", fields[i].Name, mustJSON(t, fields[i]), want)
		}
	}
	if stored.VarsMode != templates.ModeManual || stored.VarsSourceRev != "c1" || stored.VarsAppliedAt == nil ||
		stored.Revision != 2 || applied.Revision != 2 || stored.ExtraVars["release"] != "1.0" {
		t.Fatalf("stored = mode %q rev %q at %v revision %d vars %v", stored.VarsMode, stored.VarsSourceRev, stored.VarsAppliedAt, stored.Revision, stored.ExtraVars)
	}

	view, err := f.svc.Variables(ctx, f.org, tm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source.AppliedRev != "c1" || view.Source.Newer || view.Template.Revision != 2 {
		t.Fatalf("view source = %+v", view.Source)
	}
	if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeOff, SourceRev: "c1", SourceSyncedAt: &syncedAt}); !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("apply with mode off: %v, want ErrInvalidInput", err)
	}
}

// TestApplyRefusesAChangedSource: the person previewed another revision
// than the one the apply would use.
func TestApplyRefusesAChangedSource(t *testing.T) {
	f := newFixture()
	f.sources[f.playbook] = readSource(uuid.Must(uuid.NewV7()), "c2", candidate("region", schema.TypeString))
	tm, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &syncedAt})
	if !errors.Is(err, templates.ErrSourceChanged) {
		t.Fatalf("apply of c1 over c2: %v, want ErrSourceChanged", err)
	}
	if f.store.byID[tm.ID].Revision != 1 {
		t.Fatal("a refused apply wrote")
	}
}

// TestApplyUnavailableSourceIsRefused: nothing is linked to a source
// that was not read.
func TestApplyUnavailableSourceIsRefused(t *testing.T) {
	f := newFixture()
	src := readSource(uuid.Must(uuid.NewV7()), "c1", candidate("region", schema.TypeString))
	src.WorkerReads = false
	f.sources[f.playbook] = src
	tm, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	p, info, err := f.svc.PreviewVariables(ctx, f.org, tm.ID)
	if err != nil || len(p.Added) != 0 || info.Available || info.Reason == "" || p.Notes[0].Code != "source_unavailable" {
		t.Fatalf("preview = %+v, source %+v, err %v", p, info, err)
	}
	_, err = f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &syncedAt})
	if !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("apply of an unavailable source: %v, want ErrInvalidInput", err)
	}
}

// TestApplyRequiresReEnteredValues: a person's apply validates and seals
// the values under the new fields; a value that no longer fits is
// entered again in the same apply.
func TestApplyRequiresReEnteredValues(t *testing.T) {
	repo := uuid.Must(uuid.NewV7())
	t.Run("string to int", func(t *testing.T) {
		f := newFixture()
		port := candidate("port", schema.TypeString)
		tm := f.linked(t, "port", templates.ModeManual, "c1", []schema.Field{asStored(port, true)}, map[string]any{"port": "eighty"})
		f.sources[f.playbook] = readSource(repo, "c2", candidate("port", schema.TypeInt))
		_, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c2", SourceSyncedAt: &syncedAt})
		if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"port"}) {
			t.Fatalf("problems %v, want port", got)
		}
		if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{
			Mode: templates.ModeManual, SourceRev: "c2", SourceSyncedAt: &syncedAt, Values: map[string]any{"port": float64(80)},
		}); err != nil {
			t.Fatal(err)
		}
		stored := f.store.byID[tm.ID]
		if stored.ExtraVars["port"] != float64(80) || stored.ExtraVarsSchema.Fields[0].Type != schema.TypeInt {
			t.Fatalf("stored %v %s", stored.ExtraVars, mustJSON(t, stored.ExtraVarsSchema))
		}
	})
	t.Run("secret to string", func(t *testing.T) {
		f := newFixture()
		token := candidate("token", schema.TypeSecret)
		tm := f.linked(t, "token", templates.ModeManual, "c1", []schema.Field{asStored(token, true)}, map[string]any{"token": "hunter2"})
		storedSealed(t, f, tm.ID, "token")
		f.sources[f.playbook] = readSource(repo, "c2", candidate("token", schema.TypeString))
		_, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c2", SourceSyncedAt: &syncedAt})
		if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"token"}) || !strings.Contains(err.Error(), "enter the value again") {
			t.Fatalf("problems %v (%v), want token to be entered again", got, err)
		}
		if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{
			Mode: templates.ModeManual, SourceRev: "c2", SourceSyncedAt: &syncedAt, Values: map[string]any{"token": "visible"},
		}); err != nil {
			t.Fatal(err)
		}
		if got := f.store.byID[tm.ID].ExtraVars["token"]; got != "visible" {
			t.Fatalf("token = %#v", got)
		}
	})
	t.Run("string to secret", func(t *testing.T) {
		f := newFixture()
		pw := candidate("pw", schema.TypeString)
		tm := f.linked(t, "pw", templates.ModeManual, "c1", []schema.Field{asStored(pw, true)}, map[string]any{"pw": "abc"})
		f.sources[f.playbook] = readSource(repo, "c2", candidate("pw", schema.TypeSecret))
		applied, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c2", SourceSyncedAt: &syncedAt})
		if err != nil {
			t.Fatal(err)
		}
		storedSealed(t, f, tm.ID, "pw")
		if m, ok := applied.ExtraVars["pw"].(map[string]any); !ok || m["$secret"] != "set" {
			t.Fatalf("read pw = %#v, want masked", applied.ExtraVars["pw"])
		}
	})
	t.Run("null removes a value", func(t *testing.T) {
		f := newFixture()
		region := candidate("region", schema.TypeString)
		old := candidate("old", schema.TypeString)
		tm := f.linked(t, "null", templates.ModeManual, "c1", []schema.Field{asStored(region, true), asStored(old, false)},
			map[string]any{"region": "eu", "old": "kept"})
		f.sources[f.playbook] = readSource(repo, "c1", region, old)
		if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{
			Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &syncedAt, Values: map[string]any{"old": nil},
		}); err != nil {
			t.Fatal(err)
		}
		if got := f.store.byID[tm.ID].ExtraVars; !reflect.DeepEqual(got, map[string]any{"region": "eu"}) {
			t.Fatalf("vars = %v, want old removed and region kept", got)
		}
	})
}

// TestApplyRemovesGoneFieldsOnlyWhenAsked: a person's apply keeps a gone
// field unless the removal is confirmed; its value stays stored.
func TestApplyRemovesGoneFieldsOnlyWhenAsked(t *testing.T) {
	f := newFixture()
	keep := candidate("keep", schema.TypeString)
	old := candidate("old", schema.TypeString)
	tm := f.linked(t, "gone", templates.ModeManual, "c1", []schema.Field{asStored(keep, true), asStored(old, true)},
		map[string]any{"keep": "k", "old": "o"})
	f.sources[f.playbook] = readSource(uuid.Must(uuid.NewV7()), "c2", keep)
	if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c2", SourceSyncedAt: &syncedAt}); err != nil {
		t.Fatal(err)
	}
	if fields := f.store.byID[tm.ID].ExtraVarsSchema.Fields; len(fields) != 2 || fields[1].Origin != schema.OriginGone {
		t.Fatalf("fields = %s, want old marked gone", mustJSON(t, fields))
	}
	if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeAutomatic, SourceRev: "c2", SourceSyncedAt: &syncedAt, RemoveGone: true}); err != nil {
		t.Fatal(err)
	}
	stored := f.store.byID[tm.ID]
	if !reflect.DeepEqual(fieldNames(stored.ExtraVarsSchema.Fields), []string{"keep"}) || stored.ExtraVars["old"] != "o" || stored.VarsMode != templates.ModeAutomatic {
		t.Fatalf("fields %v vars %v mode %q, want old removed, its value kept", fieldNames(stored.ExtraVarsSchema.Fields), stored.ExtraVars, stored.VarsMode)
	}
}

// TestUnlinkMakesFieldsManual: mode off turns every field manual, keeps
// the active switches and the values, and forgets the applied revision;
// relinking needs a preview again. Between manual and automatic the
// mode changes freely.
func TestUnlinkMakesFieldsManual(t *testing.T) {
	f := newFixture()
	src := asStored(candidate("src", schema.TypeString, setInPlaybook), false)
	adj := candidate("adj", schema.TypeString)
	adj.Label, adj.Origin = "Mine", schema.OriginAdjusted
	adj.SourceField = &schema.Field{Name: "adj", Type: schema.TypeString}
	gone := candidate("gone", schema.TypeSecret)
	gone.Origin = schema.OriginGone
	tm := f.linked(t, "unlink", templates.ModeManual, "c1", []schema.Field{src, adj, gone}, map[string]any{"adj": "a", "gone": "s3cret"})
	sealed := storedSealed(t, f, tm.ID, "gone")

	auto, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeAutomatic, rev(1))
	if err != nil || auto.VarsMode != templates.ModeAutomatic || auto.Revision != 2 || auto.VarsSourceRev != "c1" {
		t.Fatalf("to automatic: %+v %v", auto, err)
	}
	if same, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeAutomatic, rev(2)); err != nil || same.Revision != 2 || f.store.byID[tm.ID].Revision != 2 {
		t.Fatalf("to the current mode: revision %d, %v; want no write", same.Revision, err)
	}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeAutomatic, rev(1)); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("to the current mode with a stale revision: %v, want ErrRevisionMismatch", err)
	}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeOff, rev(1)); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("stale mode change: %v, want ErrRevisionMismatch", err)
	}
	off, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeOff, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored := f.store.byID[tm.ID]
	if off.VarsMode != templates.ModeOff || stored.VarsSourceRev != "" || stored.VarsAppliedAt != nil || stored.Revision != 3 {
		t.Fatalf("off = mode %q rev %q revision %d", stored.VarsMode, stored.VarsSourceRev, stored.Revision)
	}
	for _, fl := range stored.ExtraVarsSchema.Fields {
		if fl.EffectiveOrigin() != schema.OriginManual || fl.Source != nil || fl.SourceField != nil || fl.SetInPlaybook {
			t.Errorf("field %s = %s, want manual without provenance", fl.Name, mustJSON(t, fl))
		}
	}
	if fields := stored.ExtraVarsSchema.Fields; fields[0].IsActive() || fields[1].Label != "Mine" || fields[2].Type != schema.TypeSecret {
		t.Errorf("fields = %s, want content and active switches kept", mustJSON(t, fields))
	}
	if err := stored.ExtraVarsSchema.Check(); err != nil {
		t.Errorf("Check: %v", err)
	}
	if got := storedSealed(t, f, tm.ID, "gone"); string(got) != string(sealed) || stored.ExtraVars["adj"] != "a" {
		t.Errorf("values changed: %v", stored.ExtraVars)
	}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeAutomatic, nil); !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("relink without a preview: %v, want ErrInvalidInput", err)
	}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, templates.ModeOff, nil); err != nil || f.store.byID[tm.ID].Revision != 3 {
		t.Fatalf("off again: revision %d, %v; want no write", f.store.byID[tm.ID].Revision, err)
	}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, tm.ID, "sometimes", nil); !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("unknown mode: %v, want ErrInvalidInput", err)
	}
}

// TestProjectedTemplateCannotLink: a projection's variables belong to its
// installation-owned definition.
func TestProjectedTemplateCannotLink(t *testing.T) {
	f := newFixture()
	f.sources[f.playbook] = readSource(uuid.Must(uuid.NewV7()), "c1", candidate("region", schema.TypeString))
	shared := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	f.store.byID[id] = templates.Template{ID: id, OrgID: f.org, PlaybookID: f.playbook, Name: "proj",
		SharedTemplateID: &shared, ExtraVars: map[string]any{}, Revision: 1, VarsMode: templates.ModeOff}
	if _, err := f.svc.SetVariablesMode(ctx, f.org, id, templates.ModeOff, nil); !errors.Is(err, templates.ErrForbidden) {
		t.Fatalf("mode: %v, want ErrForbidden", err)
	}
	if _, _, err := f.svc.PreviewVariables(ctx, f.org, id); !errors.Is(err, templates.ErrForbidden) {
		t.Fatalf("preview: %v, want ErrForbidden", err)
	}
	if _, err := f.svc.ApplyVariables(ctx, f.org, id, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &syncedAt}); !errors.Is(err, templates.ErrForbidden) {
		t.Fatalf("apply: %v, want ErrForbidden", err)
	}
	if f.store.byID[id].Revision != 1 {
		t.Fatal("a projection was written")
	}
}

// TestListReviewsLinkedTemplates: the list carries a review summary for
// each linked template, and none for an unlinked one.
func TestListReviewsLinkedTemplates(t *testing.T) {
	f := newFixture()
	keep := candidate("keep", schema.TypeString)
	old := candidate("old", schema.TypeString)
	linked := f.linked(t, "a-linked", templates.ModeManual, "c1", []schema.Field{asStored(keep, true), asStored(old, true)},
		map[string]any{"keep": "k", "old": "o"})
	f.sources[f.playbook] = readSource(uuid.Must(uuid.NewV7()), "c2", keep, candidate("new", schema.TypeString, required))
	unlinked, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: "b-unlinked", PlaybookID: f.playbook})
	if err != nil {
		t.Fatal(err)
	}
	list, reviews, err := f.svc.ListReviewed(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != linked.ID {
		t.Fatalf("list = %+v", list)
	}
	if _, ok := reviews[unlinked.ID]; ok {
		t.Errorf("an unlinked template has a review: %+v", reviews)
	}
	// Nothing is marked gone yet: the preview alone does not count.
	if got, ok := reviews[linked.ID]; !ok || got != (templates.Review{}) {
		t.Errorf("review = %+v (listed %v), want nothing to review before an apply", got, ok)
	}
}

// TestApplyRefusesASourceSyncedAgain: a sync whose commit the worker
// could not read keeps the previous commit with new content, so the sync
// time tells the person's preview apart from the current read.
func TestApplyRefusesASourceSyncedAgain(t *testing.T) {
	f := newFixture()
	src := readSource(uuid.Must(uuid.NewV7()), "c1", candidate("region", schema.TypeString))
	later := syncedAt.Add(time.Hour)
	src.SyncedAt = &later
	f.sources[f.playbook] = src
	tm, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []*time.Time{&syncedAt, nil} {
		_, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: at})
		if !errors.Is(err, templates.ErrSourceChanged) {
			t.Fatalf("apply of a preview synced at %v: %v, want ErrSourceChanged", at, err)
		}
	}
	same := later.In(time.FixedZone("x", 3600))
	if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, templates.ApplyInput{Mode: templates.ModeManual, SourceRev: "c1", SourceSyncedAt: &same}); err != nil {
		t.Fatalf("apply of the current read: %v", err)
	}
}

// TestListReviewsWithoutAnUnreadableSource: a playbook whose sources
// cannot be read costs its templates their review, not the list; each
// playbook is read once.
func TestListReviewsWithoutAnUnreadableSource(t *testing.T) {
	f := newFixture()
	var logs bytes.Buffer
	f.svc.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	field := []schema.Field{asStored(candidate("region", schema.TypeString), true)}
	a := f.linked(t, "a", templates.ModeManual, "c1", field, nil)
	b := f.linked(t, "b", templates.ModeAutomatic, "c1", field, nil)
	c := f.linkedTo(t, f.playbook2, "c", templates.ModeManual, "c1", field, nil)
	f.sources[f.playbook2] = readSource(uuid.Must(uuid.NewV7()), "c1", field[0])
	f.sourceErrs[f.playbook] = errors.New("database unavailable")
	list, reviews, err := f.svc.ListReviewed(ctx, f.org)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %d templates, %v", len(list), err)
	}
	if _, ok := reviews[a.ID]; ok {
		t.Errorf("a has a review")
	}
	if _, ok := reviews[b.ID]; ok {
		t.Errorf("b has a review")
	}
	if _, ok := reviews[c.ID]; !ok {
		t.Errorf("c has no review")
	}
	if f.sourceReads[f.playbook] != 1 || f.sourceReads[f.playbook2] != 1 {
		t.Errorf("reads = %v, want one per playbook", f.sourceReads)
	}
	if !strings.Contains(logs.String(), "database unavailable") {
		t.Errorf("logs = %q", logs.String())
	}
}
