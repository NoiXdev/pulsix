package templates_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestIDsReachingFollowsChainsBackwards (final review 1): a change of C
// reaches B (B starts C) and A (A starts B), not D (C starts D, which runs
// after C, not into it), and not a template of another organization.
func TestIDsReachingFollowsChainsBackwards(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-reaching")
	scoped := db.WithOrgScope(ctx, org)
	pb := seedEditorPlaybook(t, d, org)
	store := templates.NewSQLStore(d)
	ids := map[string]uuid.UUID{}
	for _, name := range []string{"a", "b", "c", "d", "lone"} {
		created, err := store.Create(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: pb, Name: name, ExtraVars: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = created.ID
	}
	for from, to := range map[string]string{"a": "b", "b": "c", "c": "d"} {
		if err := store.ReplaceFollowUps(scoped, org, ids[from], []uuid.UUID{ids[to]}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.IDsReaching(scoped, org, []uuid.UUID{ids["c"]})
	if err != nil {
		t.Fatal(err)
	}
	want := []uuid.UUID{ids["a"], ids["b"], ids["c"]}
	slices.SortFunc(want, func(x, y uuid.UUID) int { return slices.Compare(x[:], y[:]) })
	if !slices.Equal(got, want) {
		t.Fatalf("reaching c = %v, want %v", got, want)
	}
	if got, err := store.IDsReaching(scoped, org, []uuid.UUID{ids["lone"], uuid.Must(uuid.NewV7())}); err != nil || !slices.Equal(got, []uuid.UUID{ids["lone"]}) {
		t.Fatalf("reaching lone = %v, %v", got, err)
	}
}
