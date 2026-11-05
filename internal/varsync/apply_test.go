package varsync_test

import (
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
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
	once := varsync.Apply(current, p, varsync.ApplyOptions{Automatic: true})
	again := varsync.Compute(once, source(p.Added[0].Field, field(t, once, "nginx_port"),
		marker("mode", schema.TypeEnum, 4, func(f *schema.Field) { f.Options = []string{"green"} })), map[string]any{"mode": "blue"})
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
func TestPreviewAppliedRev(t *testing.T) {
	cur := []schema.Field{stored(marker("release", schema.TypeString, 3), true)}
	if got := varsync.Compute(cur, source(), nil).AppliedRev("c0"); got != "c1" {
		t.Errorf("available: AppliedRev = %q, want c1", got)
	}
	leftOut := varsync.NewSource(varsync.Read{Found: true, WorkerReads: true, LeftOut: true})
	if got := varsync.Compute(cur, leftOut, nil).AppliedRev("c0"); got != "c0" {
		t.Errorf("left out: AppliedRev = %q, want c0", got)
	}
	failed := source()
	failed.Available = false
	if got := varsync.Compute(cur, failed, nil).AppliedRev("c0"); got != "c0" {
		t.Errorf("unavailable with a commit: AppliedRev = %q, want c0", got)
	}
	emptyRev := source()
	emptyRev.Rev = ""
	if got := varsync.Compute(cur, emptyRev, nil).AppliedRev("c0"); got != "c0" {
		t.Errorf("empty revision: AppliedRev = %q, want c0", got)
	}
}
