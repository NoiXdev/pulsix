package varsync_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
	"github.com/noixdev/pulsix/internal/varsync"
)

// syncCase is a template with a manual field, a field whose change is
// safe, one whose change needs review and one gone from the source, and a
// source that also adds a field.
func syncCase() ([]schema.Field, varsync.Preview) {
	port := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })
	mode := marker("mode", schema.TypeEnum, 4, func(f *schema.Field) { f.Options = []string{"blue", "green"} })
	old := marker("old", schema.TypeString, 5)
	current := []schema.Field{
		manual("mine", schema.TypeString),
		stored(port, false),
		stored(mode, true),
		stored(old, true),
	}
	newPort := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 8080 })
	newMode := marker("mode", schema.TypeEnum, 4, func(f *schema.Field) { f.Options = []string{"green"} })
	release := marker("release", schema.TypeString, 3, func(f *schema.Field) { f.Required = true })
	p := varsync.Compute(current, source(release, newPort, newMode), map[string]any{"mode": "blue"})
	return current, p
}

func TestApplyAutomaticHoldsReviewAndRemovals(t *testing.T) {
	current, p := syncCase()
	if len(p.Changed) != 2 || !p.Changed[1].NeedsReview || p.Changed[0].NeedsReview {
		t.Fatalf("changed = %s", jsonOf(t, p.Changed))
	}

	out := varsync.Apply(current, p, varsync.ApplyOptions{Automatic: true, RemoveGone: true})

	got := names(out, func(f schema.Field) string { return f.Name })
	if want := []string{"mine", "nginx_port", "mode", "old", "release"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
	if f := field(t, out, "release"); !*f.Active || f.Origin != schema.OriginSource {
		t.Errorf("added = %s", jsonOf(t, f))
	}
	if f := field(t, out, "nginx_port"); jsonOf(t, f.Default) != "8080" {
		t.Errorf("safe change not applied: %s", jsonOf(t, f))
	}
	if f := field(t, out, "mode"); !reflect.DeepEqual(f.Options, []string{"blue", "green"}) {
		t.Errorf("change needing review applied: %s", jsonOf(t, f))
	}
	if f := field(t, out, "old"); f.Origin != schema.OriginGone {
		t.Errorf("gone field = %s, want kept and marked gone", jsonOf(t, f))
	}
	if !reflect.DeepEqual(out[0], current[0]) {
		t.Errorf("manual field changed: %s", jsonOf(t, out[0]))
	}
	if err := (schema.Schema{Fields: out}).Check(); err != nil {
		t.Errorf("applied schema does not pass Check: %v", err)
	}
}

func TestApplyPersonRemovesGoneOnlyWhenAsked(t *testing.T) {
	current, p := syncCase()

	kept := varsync.Apply(current, p, varsync.ApplyOptions{})
	if f := field(t, kept, "old"); f.Origin != schema.OriginGone {
		t.Errorf("gone field = %s, want marked gone", jsonOf(t, f))
	}
	if f := field(t, kept, "mode"); !reflect.DeepEqual(f.Options, []string{"green"}) {
		t.Errorf("a person's apply holds back a reviewed change: %s", jsonOf(t, f))
	}

	removed := varsync.Apply(current, p, varsync.ApplyOptions{RemoveGone: true})
	got := names(removed, func(f schema.Field) string { return f.Name })
	if want := []string{"mine", "nginx_port", "mode", "release"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

func TestApplyNeverChangesActive(t *testing.T) {
	current, p := syncCase()
	for _, opt := range []varsync.ApplyOptions{{Automatic: true}, {}, {RemoveGone: true}} {
		out := varsync.Apply(current, p, opt)
		if f := field(t, out, "nginx_port"); f.Active == nil || *f.Active {
			t.Errorf("%+v: inactive changed field = %s, want still inactive", opt, jsonOf(t, f))
		}
		if f := field(t, out, "mode"); f.Active == nil || !*f.Active {
			t.Errorf("%+v: active field = %s, want still active", opt, jsonOf(t, f))
		}
	}
	// Even a preview that claims otherwise does not switch an existing field.
	p.Changed[0].Field.Active = ptr(true)
	if f := field(t, varsync.Apply(current, p, varsync.ApplyOptions{}), "nginx_port"); *f.Active {
		t.Errorf("Apply took active from the preview: %s", jsonOf(t, f))
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	current, p := syncCase()
	once := roundTrip(t, varsync.Apply(current, p, varsync.ApplyOptions{Automatic: true})) // as stored
	again := varsync.Compute(once, source(p.Added[0].Field, field(t, once, "nginx_port"),
		marker("mode", schema.TypeEnum, 4, func(f *schema.Field) { f.Options = []string{"green"} })), map[string]any{"mode": "blue"})
	if n := len(again.Added) + len(again.Adjusted) + again.Unchanged; n != 2 || len(again.Changed) != 1 || len(again.Refresh) != 0 {
		t.Errorf("second preview = %s", jsonOf(t, again))
	}
	twice := varsync.Apply(once, again, varsync.ApplyOptions{Automatic: true})
	if jsonOf(t, twice) != jsonOf(t, once) {
		t.Errorf("second apply changed the fields:\n%s\n%s", jsonOf(t, once), jsonOf(t, twice))
	}
}

// Apply works from a preview computed against the same fields; a stale one
// never touches a field a person made manual or removed meanwhile.
func TestApplyStalePreviewLeavesManualFields(t *testing.T) {
	current, p := syncCase()
	now := []schema.Field{current[0], manual("nginx_port", schema.TypeInt), manual("release", schema.TypeString)}
	out := varsync.Apply(now, p, varsync.ApplyOptions{RemoveGone: true})
	if jsonOf(t, out) != jsonOf(t, now) {
		t.Errorf("applied = %s, want %s", jsonOf(t, out), jsonOf(t, now))
	}
}

func TestNewer(t *testing.T) {
	avail := varsync.Source{Rev: "c2", Available: true}
	cases := []struct {
		name    string
		applied string
		src     varsync.Source
		want    bool
	}{
		{"a new commit", "c1", avail, true},
		{"the applied commit", "c2", avail, false},
		{"never applied", "", avail, true},
		{"an empty revision is never newer", "c1", varsync.Source{Available: true}, false},
		{"a left-out playbook", "c1", varsync.Source{}, false},
		{"an unavailable source with a commit", "c1", varsync.Source{Rev: "c2"}, false},
	}
	for _, tc := range cases {
		if got := varsync.Newer(tc.applied, tc.src); got != tc.want {
			t.Errorf("%s: Newer = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The revision a template records after an apply never becomes empty, nor
// names a read that was not available.
func TestAppliedRev(t *testing.T) {
	leftOut := varsync.NewSource(varsync.Read{Found: true, WorkerReads: true, LeftOut: true})
	failed := source()
	failed.Available = false
	emptyRev := source()
	emptyRev.Rev = ""
	cases := []struct {
		name     string
		previous string
		src      varsync.Source
		want     string
	}{
		{"available", "c0", source(), "c1"},
		{"first link", "", source(), "c1"},
		{"left out", "c0", leftOut, "c0"},
		{"unavailable with a commit", "c0", failed, "c0"},
		{"available without a revision", "c0", emptyRev, "c0"},
	}
	for _, tc := range cases {
		if got := varsync.AppliedRev(tc.previous, tc.src); got != tc.want {
			t.Errorf("%s: AppliedRev = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A preview decoded from JSON applies its items like the one Compute
// returned; only the provenance of unchanged fields stays behind.
func TestPreviewJSONRoundTrip(t *testing.T) {
	current, p := syncCase()
	var decoded varsync.Preview
	if err := json.Unmarshal([]byte(jsonOf(t, p)), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Refresh != nil || jsonOf(t, decoded) != jsonOf(t, p) {
		t.Fatalf("decoded = %s", jsonOf(t, decoded))
	}
	for _, opt := range []varsync.ApplyOptions{{Automatic: true}, {}, {RemoveGone: true}} {
		want := jsonOf(t, varsync.Apply(current, p, opt))
		if got := jsonOf(t, varsync.Apply(roundTrip(t, current), decoded, opt)); got != want {
			t.Errorf("%+v: decoded preview applied\n%s\nwant\n%s", opt, got, want)
		}
	}

	moved := []schema.Field{stored(marker("release", schema.TypeString, 3), true)}
	q := varsync.Compute(moved, source(marker("release", schema.TypeString, 9)), nil)
	var dq varsync.Preview
	if err := json.Unmarshal([]byte(jsonOf(t, q)), &dq); err != nil {
		t.Fatal(err)
	}
	if got := varsync.Apply(moved, dq, varsync.ApplyOptions{}); got[0].Source.Line != 3 {
		t.Errorf("decoded preview moved the source: %s", jsonOf(t, got))
	}
	if got := varsync.Apply(moved, q, varsync.ApplyOptions{}); got[0].Source.Line != 9 {
		t.Errorf("Compute's preview did not move the source: %s", jsonOf(t, got))
	}
}

// A preview older than the fields never overwrites what a person did in
// between: an adjustment, a field made manual, or an adjusted field's
// content.
func TestApplyStalePreviewKeepsAdjustments(t *testing.T) {
	old := marker("port", schema.TypeInt, 3, func(f *schema.Field) { f.Default = 80 })
	gone1 := marker("gone1", schema.TypeString, 4)
	adj := marker("adj", schema.TypeString, 5)
	mineAdj := adj
	mineAdj.Label = "Mine"
	cur := []schema.Field{stored(old, true), stored(gone1, true), adjusted(stored(mineAdj, true), adj)}
	adj2 := marker("adj", schema.TypeString, 6, func(f *schema.Field) { f.Description = "new" })
	p := varsync.Compute(cur, source(marker("port", schema.TypeInt, 3, func(f *schema.Field) { f.Default = 81 }), adj2), nil)
	if len(p.Changed) != 1 || len(p.Gone) != 1 || len(p.Adjusted) != 1 {
		t.Fatalf("preview = %s", jsonOf(t, p))
	}

	minePort := old
	minePort.Label = "Mine"
	edited := mineAdj
	edited.Label = "Edited"
	now := []schema.Field{adjusted(stored(minePort, true), old), adjusted(stored(gone1, true), gone1), adjusted(stored(edited, true), adj)}
	for _, opt := range []varsync.ApplyOptions{{Automatic: true}, {RemoveGone: true}} {
		out := varsync.Apply(now, p, opt)
		if len(out) != 3 {
			t.Fatalf("%+v: applied = %s", opt, jsonOf(t, out))
		}
		if out[0].Label != "Mine" || out[0].Origin != schema.OriginAdjusted || jsonOf(t, out[0].Default) != "80" {
			t.Errorf("%+v: stale change overwrote an adjustment: %s", opt, jsonOf(t, out[0]))
		}
		if out[1].Origin != schema.OriginAdjusted {
			t.Errorf("%+v: stale gone item touched an adjusted field: %s", opt, jsonOf(t, out[1]))
		}
		if out[2].Label != "Edited" || out[2].SourceField.Description != "new" || out[2].Source.Line != 6 {
			t.Errorf("%+v: adjusted field = %s, want the person's content with the new source version", opt, jsonOf(t, out[2]))
		}
	}
}

// A template holds at most schema.MaxFields fields: what does not fit is
// left out with a warning, and Apply's result passes Check.
func TestComputeStaysWithinTheFieldLimit(t *testing.T) {
	t.Run("first link beside manual fields", func(t *testing.T) {
		var cur, cands []schema.Field
		for i := range 10 {
			cur = append(cur, manual(fmt.Sprintf("mine_%d", i), schema.TypeString))
		}
		for i := range 100 {
			cands = append(cands, marker(fmt.Sprintf("v_%d", i), schema.TypeString, i+1))
		}
		p := varsync.Compute(cur, source(cands...), nil)
		if len(p.Added) != 90 || p.Added[89].Field.Name != "v_89" {
			t.Errorf("added %d fields", len(p.Added))
		}
		checkLeftOut(t, p, 10, "v_90, v_91")
		for _, opt := range []varsync.ApplyOptions{{Automatic: true}, {}} {
			if err := (schema.Schema{Fields: varsync.Apply(cur, p, opt)}).Check(); err != nil {
				t.Errorf("%+v: Check: %v", opt, err)
			}
		}
	})
	t.Run("automatic rename keeps the gone fields", func(t *testing.T) {
		var cur, renamed []schema.Field
		for i := range 60 {
			cur = append(cur, stored(marker(fmt.Sprintf("old_%d", i), schema.TypeString, i+1), false))
			renamed = append(renamed, marker(fmt.Sprintf("new_%d", i), schema.TypeString, i+1))
		}
		p := varsync.Compute(cur, source(renamed...), nil)
		if len(p.Added) != 40 || len(p.Gone) != 60 {
			t.Errorf("added %d gone %d", len(p.Added), len(p.Gone))
		}
		checkLeftOut(t, p, 20, "new_40, new_41, new_42, new_43, new_44, new_45, new_46, new_47, new_48, new_49 and 10 more")
		out := varsync.Apply(cur, p, varsync.ApplyOptions{Automatic: true})
		if err := (schema.Schema{Fields: out}).Check(); err != nil {
			t.Errorf("Check: %v", err)
		}
		// Once a person removed the gone fields, the rest fits.
		removed := varsync.Apply(cur, p, varsync.ApplyOptions{RemoveGone: true})
		next := varsync.Compute(removed, source(renamed...), nil)
		if len(next.Added) != 20 || hasCode(next, "fields_left_out") || next.LeftOut != 0 {
			t.Errorf("after removing: added %d, notes %v", len(next.Added), noteCodes(next))
		}
	})
	t.Run("a stale preview cannot overflow either", func(t *testing.T) {
		var cands, full []schema.Field
		for i := range 5 {
			cands = append(cands, marker(fmt.Sprintf("v_%d", i), schema.TypeString, i+1))
		}
		for i := range schema.MaxFields {
			full = append(full, manual(fmt.Sprintf("m_%d", i), schema.TypeString))
		}
		p := varsync.Compute(nil, source(cands...), nil)
		if out := varsync.Apply(full, p, varsync.ApplyOptions{}); len(out) != schema.MaxFields {
			t.Errorf("applied %d fields", len(out))
		}
	})
}

func checkLeftOut(t *testing.T, p varsync.Preview, count int, listed string) {
	t.Helper()
	if p.LeftOut != count {
		t.Errorf("LeftOut = %d, want %d", p.LeftOut, count)
	}
	want := []string{fmt.Sprintf("%d fields", count), listed, "remove gone fields and preview again"}
	for _, n := range p.Notes {
		if n.Code == "fields_left_out" {
			for _, w := range want {
				if n.Level != varsource.LevelWarning || !strings.Contains(n.Message, w) {
					t.Errorf("note = %+v, want %q", n, w)
				}
			}
			return
		}
	}
	t.Errorf("notes = %v, want fields_left_out", noteCodes(p))
}
