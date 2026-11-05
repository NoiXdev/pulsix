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
