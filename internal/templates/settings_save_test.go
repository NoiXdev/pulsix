package templates_test

import (
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestSettingsSaveKeepsAMissingRequiredValue: an automatic apply added a
// required field without a value. The template dialog, which echoes the
// schema and the stored values, still saves the template's other settings
// (a rename); only the variables page and an apply insist on the value.
// A settings save that changes a value is checked in full.
func TestSettingsSaveKeepsAMissingRequiredValue(t *testing.T) {
	f, repo := repoFixture()
	port := candidate("port", schema.TypeInt)
	token := candidate("token", schema.TypeSecret)
	tm := f.linked(t, "auto", templates.ModeAutomatic, "c1", []schema.Field{asStored(port, true), asStored(token, true)},
		map[string]any{"port": float64(1), "token": "hunter2"})
	f.sources[f.playbook] = readSource(repo, "c2", port, token, candidate("release", schema.TypeString, required))
	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	cur := f.store.byID[tm.ID]
	if got := fieldNames(cur.ExtraVarsSchema.Fields); !reflect.DeepEqual(got, []string{"port", "token", "release"}) {
		t.Fatalf("fields = %v, want release added", got)
	}
	sealed := storedSealed(t, f, tm.ID, "token")
	echoed := map[string]any{"port": float64(1), "token": map[string]any{"$secret": "keep", "from": "token"}}
	settings := func(name string, vars map[string]any) templates.UpdateInput {
		sc := *cur.ExtraVarsSchema
		return templates.UpdateInput{Name: name, PlaybookID: cur.PlaybookID, ExtraVarsSchema: &sc, ExtraVars: vars}
	}

	if _, err := f.svc.Update(ctx, f.org, tm.ID, settings("renamed", echoed)); err != nil {
		t.Fatalf("rename with the values echoed: %v", err)
	}
	if got := f.store.byID[tm.ID]; got.Name != "renamed" || string(storedSealed(t, f, tm.ID, "token")) != string(sealed) {
		t.Fatalf("after the rename: name %q vars %v", got.Name, got.ExtraVars)
	}

	_, err := f.svc.Update(ctx, f.org, tm.ID, settings("renamed", map[string]any{"port": float64(2), "token": echoed["token"]}))
	if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"release"}) {
		t.Fatalf("a settings save that changes a value: problems %v, want release", got)
	}
	_, err = f.svc.Update(ctx, f.org, tm.ID, settings("renamed", map[string]any{"port": "one", "token": echoed["token"]}))
	if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"port", "release"}) {
		t.Fatalf("a settings save with a bad value: problems %v, want port and release", got)
	}
	sc := *f.store.byID[tm.ID].ExtraVarsSchema
	_, err = f.svc.UpdateVariables(ctx, f.org, tm.ID, templates.VariablesInput{Schema: &sc, ExtraVars: echoed})
	if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"release"}) {
		t.Fatalf("the variables page's save: problems %v, want release", got)
	}
	echoed["release"] = "1.0"
	if _, err := f.svc.UpdateVariables(ctx, f.org, tm.ID, templates.VariablesInput{Schema: &sc, ExtraVars: echoed}); err != nil {
		t.Fatalf("the variables page's save with the value: %v", err)
	}
}

// TestSettingsSaveKeepsAMissingRequiredValueOverALegacySecret is the
// settings save above over a legacy template whose token is stored in
// plaintext (backlog 2.47): the save reseals it, which must not count as
// a changed value (backlog 7.87). A save that does change it is checked in
// full.
func TestSettingsSaveKeepsAMissingRequiredValueOverALegacySecret(t *testing.T) {
	f, repo := repoFixture()
	port := candidate("port", schema.TypeInt)
	token := candidate("token", schema.TypeSecret)
	tm := f.linked(t, "auto", templates.ModeAutomatic, "c1", []schema.Field{asStored(port, true), asStored(token, true)},
		map[string]any{"port": float64(1), "token": "hunter2"})
	f.sources[f.playbook] = readSource(repo, "c2", port, token, candidate("release", schema.TypeString, required))
	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	cur := f.store.byID[tm.ID]
	legacy := func() { revertToLegacy(t, f, cur, map[string]any{"port": float64(1), "token": "hunter2"}) }
	keep := map[string]any{"$secret": "keep", "from": "token"}
	settings := func(name string, vars map[string]any) templates.UpdateInput {
		sc := *cur.ExtraVarsSchema
		return templates.UpdateInput{Name: name, PlaybookID: cur.PlaybookID, ExtraVarsSchema: &sc, ExtraVars: vars}
	}

	legacy()
	if _, err := f.svc.Update(ctx, f.org, tm.ID, settings("renamed", map[string]any{"port": float64(1), "token": keep})); err != nil {
		t.Fatalf("rename with the values echoed over a legacy secret: %v", err)
	}
	if got := f.store.byID[tm.ID]; got.Name != "renamed" {
		t.Fatalf("after the rename: name %q", got.Name)
	}
	if got := openStored(t, testBox(), f, cur, "token"); got != "hunter2" {
		t.Fatalf("stored token opens to %q, want the legacy value sealed", got)
	}

	legacy()
	_, err := f.svc.Update(ctx, f.org, tm.ID, settings("again", map[string]any{"port": float64(1), "token": "other"}))
	if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"release"}) {
		t.Fatalf("a settings save that changes the legacy secret: problems %v, want release", got)
	}
	if got := f.store.byID[tm.ID]; got.Name != "renamed" || got.ExtraVars["token"] != "hunter2" {
		t.Fatalf("a refused save changed the template: name %q", got.Name)
	}
}

// TestPlaybookChangeUnlinksTheAppliedSource: a template whose playbook a
// person changes follows the new playbook only after a preview: automatic
// mode turns manual, the applied source revision is forgotten, and the
// next sync leaves the template alone. Manual stays manual, off stays off,
// and a save that keeps the playbook keeps the link.
func TestPlaybookChangeUnlinksTheAppliedSource(t *testing.T) {
	port := candidate("port", schema.TypeInt)
	for _, tc := range []struct {
		name   string
		mode   templates.SourceMode
		change bool
		want   templates.SourceMode
		rev    string
	}{
		{"automatic, playbook changed", templates.ModeAutomatic, true, templates.ModeManual, ""},
		{"manual, playbook changed", templates.ModeManual, true, templates.ModeManual, ""},
		{"off, playbook changed", templates.ModeOff, true, templates.ModeOff, ""},
		{"automatic, playbook kept", templates.ModeAutomatic, false, templates.ModeAutomatic, "c1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, repo := repoFixture()
			rev := "c1"
			if tc.mode == templates.ModeOff {
				rev = ""
			}
			tm := f.linked(t, "tpl", tc.mode, rev, []schema.Field{asStored(port, true)}, map[string]any{"port": float64(1)})
			at := syncedAt
			stored := f.store.byID[tm.ID]
			stored.VarsAppliedAt = &at
			f.store.byID[tm.ID] = stored
			playbook := tm.PlaybookID
			if tc.change {
				playbook = f.playbook2
			}
			updated, err := f.svc.Update(ctx, f.org, tm.ID, templates.UpdateInput{Name: "tpl", PlaybookID: playbook,
				ExtraVarsSchema: tm.ExtraVarsSchema, ExtraVars: map[string]any{"port": float64(1)}})
			if err != nil {
				t.Fatal(err)
			}
			got := f.store.byID[tm.ID]
			if updated.VarsMode != tc.want || got.VarsMode != tc.want || got.VarsSourceRev != tc.rev || (got.VarsAppliedAt == nil) != (tc.rev == "") {
				t.Fatalf("mode %q (returned %q) rev %q applied %v, want %q %q", got.VarsMode, updated.VarsMode, got.VarsSourceRev, got.VarsAppliedAt, tc.want, tc.rev)
			}
			if got.Revision != tm.Revision+1 {
				t.Errorf("revision %d, want %d: the save moves it once", got.Revision, tm.Revision+1)
			}
			f.sources[f.playbook2] = readSource(repo, "c9", candidate("other", schema.TypeString))
			if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
				t.Fatal(err)
			}
			if tc.change {
				if fields := fieldNames(f.store.byID[tm.ID].ExtraVarsSchema.Fields); !reflect.DeepEqual(fields, []string{"port"}) {
					t.Errorf("fields after a sync = %v, want the new playbook left to a preview", fields)
				}
			}
		})
	}
}
