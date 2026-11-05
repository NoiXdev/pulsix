package varsync_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
	"github.com/noixdev/pulsix/internal/varsync"
)

func TestComputeFirstLink(t *testing.T) {
	release := marker("release", schema.TypeString, 3, func(f *schema.Field) { f.Required = true })
	port := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })
	env := marker("app_env", schema.TypeString, 4, func(f *schema.Field) { f.Required, f.SetInPlaybook = true, true })

	p := varsync.Compute(nil, source(release, port, env), nil)

	if got, want := addedNames(p), []string{"release", "nginx_port", "app_env"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("added = %v, want %v", got, want)
	}
	var active []bool
	for _, a := range p.Added {
		if a.Field.Active == nil {
			t.Fatalf("%s: added without an explicit active switch", a.Field.Name)
		}
		active = append(active, *a.Field.Active)
		if a.Field.Origin != schema.OriginSource || a.Field.Source == nil {
			t.Errorf("%s: origin %q source %v, want source with a source", a.Field.Name, a.Field.Origin, a.Field.Source)
		}
	}
	if want := []bool{true, false, false}; !reflect.DeepEqual(active, want) {
		t.Errorf("active = %v, want %v", active, want)
	}
	if !p.Added[2].Field.SetInPlaybook {
		t.Error("app_env lost set_in_playbook")
	}
	if p.SourceRev != "c1" || len(p.Changed)+len(p.Adjusted)+len(p.Gone)+p.Unchanged != 0 {
		t.Errorf("preview = %s", jsonOf(t, p))
	}
	fields := varsync.Apply(nil, p, varsync.ApplyOptions{})
	if err := (schema.Schema{Fields: fields}).Check(); err != nil {
		t.Errorf("applied schema does not pass Check: %v", err)
	}
}

func TestComputeChanged(t *testing.T) {
	old := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })
	cand := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 8080 })

	p := varsync.Compute([]schema.Field{stored(old, false)}, source(cand), map[string]any{"nginx_port": 81.0})

	if len(p.Changed) != 1 {
		t.Fatalf("changed = %s", jsonOf(t, p.Changed))
	}
	ch := p.Changed[0]
	if got, want := jsonOf(t, ch.Changes), `[{"attr":"default","old":80,"new":8080}]`; got != want {
		t.Errorf("changes = %s, want %s", got, want)
	}
	if ch.NeedsReview || ch.Reason != "" {
		t.Errorf("needs review %v (%s), want none", ch.NeedsReview, ch.Reason)
	}
	if ch.Field.Active == nil || *ch.Field.Active {
		t.Errorf("changed field active = %v, want the current false", ch.Field.Active)
	}
	if ch.Field.Origin != schema.OriginSource {
		t.Errorf("origin = %q", ch.Field.Origin)
	}
}

// A default stored as an int that comes back from the database as a float
// is the same field.
func TestComputeComparesCanonicalJSON(t *testing.T) {
	old := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80.0; f.Max = ptr(65535.0) })
	cand := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80; f.Max = ptr(65535.0) })

	p := varsync.Compute([]schema.Field{stored(old, true)}, source(cand), nil)

	if p.Unchanged != 1 || len(p.Changed) != 0 {
		t.Errorf("unchanged %d, changed %s", p.Unchanged, jsonOf(t, p.Changed))
	}
}

// An integer beyond 2^53 reads back from the database as the nearest
// float; that alone is no change.
func TestComputeBigIntegerDefaultSurvivesTheDatabase(t *testing.T) {
	cand := marker("big", schema.TypeInt, 3, func(f *schema.Field) { f.Default = int64(9007199254740993) })
	p := varsync.Compute(nil, source(cand), nil)
	stored := roundTrip(t, varsync.Apply(nil, p, varsync.ApplyOptions{Automatic: true}))
	if got := jsonOf(t, stored[0].Default); got != "9007199254740992" {
		t.Fatalf("stored default = %s, want the float it reads back as", got)
	}
	again := varsync.Compute(stored, source(cand), nil)
	if again.Unchanged != 1 || len(again.Changed) != 0 {
		t.Errorf("preview after the database = %s", jsonOf(t, again))
	}
}

func TestReviewReasons(t *testing.T) {
	str := func(name string, mod ...func(*schema.Field)) schema.Field {
		return marker(name, schema.TypeString, 5, mod...)
	}
	enum := func(opts ...string) schema.Field {
		return marker("mode", schema.TypeEnum, 5, func(f *schema.Field) { f.Options = opts })
	}
	object := func(subType schema.Type, subs ...string) schema.Field {
		f := marker("db", schema.TypeObject, 5)
		for _, s := range subs {
			f.Fields = append(f.Fields, schema.Field{Name: s, Type: subType})
		}
		return f
	}
	capped := func(s varsync.Source) varsync.Source { s.Playbook.FieldsCut = true; return s }
	deepNote := func(s varsync.Source) varsync.Source {
		s.Playbook.Notes = []varsource.Note{{Level: varsource.LevelWarning, Code: "too_deep", File: playbookPath, Line: 5, Message: "db.a.b.c.d: nested deeper than 4 levels; cut here"}}
		return s
	}
	same := func(s varsync.Source) varsync.Source { return s }
	cases := []struct {
		name   string
		old    schema.Field
		active bool
		cand   schema.Field
		values map[string]any
		src    func(varsync.Source) varsync.Source
		want   varsync.ReviewReason // "" means no review
	}{
		{"int to string", marker("port", schema.TypeInt, 5), true, str("port"), nil, same, "type"},
		{"string to secret", str("token"), true, marker("token", schema.TypeSecret, 5), nil, same, "secret"},
		{"secret to string", marker("token", schema.TypeSecret, 5), true, str("token"), nil, same, "secret"},
		{"int to secret", marker("token", schema.TypeInt, 5), true, marker("token", schema.TypeSecret, 5), nil, same, "secret"},
		{"a sub-field turns secret", object(schema.TypeString, "user", "pass"), true,
			func() schema.Field {
				f := object(schema.TypeString, "user", "pass")
				f.Fields[1].Type = schema.TypeSecret
				return f
			}(),
			nil, same, "secret"},
		{"enum loses the stored option", enum("blue", "green"), true, enum("green"), map[string]any{"mode": "blue"}, same, "value"},
		{"enum loses an option nobody stored", enum("blue", "green"), true, enum("green"), map[string]any{"mode": "green"}, same, ""},
		{"max 100 to 10 with 50 stored", marker("n", schema.TypeInt, 5, func(f *schema.Field) { f.Max = ptr(100.0) }), true,
			marker("n", schema.TypeInt, 5, func(f *schema.Field) { f.Max = ptr(10.0) }), map[string]any{"n": 50.0}, same, "value"},
		{"tighter pattern, nothing stored", str("host", func(f *schema.Field) { f.Pattern = "^[a-z]+$" }), true,
			str("host", func(f *schema.Field) { f.Pattern = "^[a-z]{3}$" }), nil, same, ""},
		{"tighter pattern, a stored null", str("host"), true,
			str("host", func(f *schema.Field) { f.Pattern = "^[a-z]{3}$" }), map[string]any{"host": nil}, same, ""},
		{"a stored value that did not fit before either", enum("green", "red"), true, enum("green"), map[string]any{"mode": "blue"}, same, ""},
		{"an inactive field's stored value no longer fits", enum("blue", "green"), false, enum("green"), map[string]any{"mode": "blue"}, same, "value"},
		{"label only", str("host"), true, str("host", func(f *schema.Field) { f.Label = "Host" }), map[string]any{"host": "x"}, same, ""},
		{"sub-field added, no cap", object(schema.TypeString, "user"), true, object(schema.TypeString, "user", "pass"), nil, same, ""},
		{"sub-field change while a cap cut fields", object(schema.TypeString, "user"), true, object(schema.TypeString, "user", "pass"), nil, capped, "cut"},
		{"sub-field change while a sub-field was cut", object(schema.TypeString, "user"), true, object(schema.TypeString, "user", "pass"), nil, deepNote, "cut"},
		{"top-level change while a cap cut fields", str("host"), true, str("host", func(f *schema.Field) { f.Label = "Host" }), nil, capped, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := varsync.Compute([]schema.Field{stored(tc.old, tc.active)}, tc.src(source(tc.cand)), tc.values)
			if len(p.Changed) != 1 {
				t.Fatalf("changed = %s", jsonOf(t, p.Changed))
			}
			ch := p.Changed[0]
			if ch.NeedsReview != (tc.want != "") || ch.Reason != tc.want {
				t.Errorf("needs review %v reason %q, want %q", ch.NeedsReview, ch.Reason, tc.want)
			}
		})
	}
}

func TestComputeAdjusted(t *testing.T) {
	src80 := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })
	mine := src80
	mine.Label = "Port"
	cand := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 8080 })
	cur := adjusted(stored(mine, true), src80)

	p := varsync.Compute([]schema.Field{cur}, source(cand), nil)

	if len(p.Adjusted) != 1 || len(p.Changed) != 0 {
		t.Fatalf("adjusted %s changed %s", jsonOf(t, p.Adjusted), jsonOf(t, p.Changed))
	}
	adj := p.Adjusted[0]
	if got, want := jsonOf(t, adj.Changes), `[{"attr":"default","old":80,"new":8080}]`; got != want {
		t.Errorf("changes = %s, want %s", got, want)
	}
	if adj.SourceField.Default != 8080 || adj.Field.Label != "Port" {
		t.Errorf("adjusted = %s", jsonOf(t, adj))
	}
	out := varsync.Apply([]schema.Field{cur}, p, varsync.ApplyOptions{Automatic: true})
	got := field(t, out, "nginx_port")
	if got.Label != "Port" || jsonOf(t, got.Default) != "80" || got.Origin != schema.OriginAdjusted {
		t.Errorf("applied = %s, want the adjustment kept", jsonOf(t, got))
	}
	if got.SourceField == nil || jsonOf(t, got.SourceField.Default) != "8080" || got.SourceField.Source != nil {
		t.Errorf("source_field = %s, want the new source version without provenance", jsonOf(t, got.SourceField))
	}
	if err := (schema.Schema{Fields: out}).Check(); err != nil {
		t.Errorf("applied schema does not pass Check: %v", err)
	}
}

func TestComputeAdjustedUnchanged(t *testing.T) {
	src80 := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80.0 })
	mine := src80
	mine.Label = "Port"
	cand := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })

	p := varsync.Compute([]schema.Field{adjusted(stored(mine, true), src80)}, source(cand), nil)

	if p.Unchanged != 1 || len(p.Adjusted) != 0 {
		t.Errorf("unchanged %d adjusted %s", p.Unchanged, jsonOf(t, p.Adjusted))
	}
}

func TestComputeReturningField(t *testing.T) {
	cand := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 8080 })
	old := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })
	mine := old
	mine.Label = "Port"

	t.Run("without a source version it returns as source", func(t *testing.T) {
		cur := gone(stored(old, false))
		p := varsync.Compute([]schema.Field{cur}, source(cand), nil)
		if len(p.Changed) != 1 {
			t.Fatalf("changed = %s", jsonOf(t, p.Changed))
		}
		ch := p.Changed[0]
		if ch.Field.Origin != schema.OriginSource || jsonOf(t, ch.Field.Default) != "8080" || ch.Field.SourceField != nil {
			t.Errorf("field = %s", jsonOf(t, ch.Field))
		}
		if got, want := jsonOf(t, ch.Changes), `[{"attr":"origin","old":"gone","new":"source"},{"attr":"default","old":80,"new":8080}]`; got != want {
			t.Errorf("changes = %s, want %s", got, want)
		}
		out := varsync.Apply([]schema.Field{cur}, p, varsync.ApplyOptions{Automatic: true})
		if f := field(t, out, "nginx_port"); f.Origin != schema.OriginSource || *f.Active {
			t.Errorf("applied = %s", jsonOf(t, f))
		}
	})
	t.Run("with a source version it returns as adjusted", func(t *testing.T) {
		cur := gone(adjusted(stored(mine, true), old))
		p := varsync.Compute([]schema.Field{cur}, source(cand), nil)
		if len(p.Changed) != 1 {
			t.Fatalf("changed = %s", jsonOf(t, p.Changed))
		}
		ch := p.Changed[0]
		if ch.Field.Origin != schema.OriginAdjusted || ch.Field.Label != "Port" || jsonOf(t, ch.Field.Default) != "80" {
			t.Errorf("field = %s", jsonOf(t, ch.Field))
		}
		if ch.Field.SourceField == nil || jsonOf(t, ch.Field.SourceField.Default) != "8080" {
			t.Errorf("source_field = %s", jsonOf(t, ch.Field.SourceField))
		}
		if ch.NeedsReview {
			t.Errorf("an adjusted field keeps its content; nothing to review: %s", ch.Reason)
		}
		out := varsync.Apply([]schema.Field{cur}, p, varsync.ApplyOptions{Automatic: true})
		if err := (schema.Schema{Fields: out}).Check(); err != nil {
			t.Errorf("applied schema does not pass Check: %v", err)
		}
	})
}

func TestComputeLeavesManualFields(t *testing.T) {
	mine := manual("release", schema.TypeString)
	mine.Label = "Mine"
	cand := marker("release", schema.TypeString, 3, func(f *schema.Field) { f.Required = true })

	p := varsync.Compute([]schema.Field{mine}, source(cand), nil)

	if len(p.Added)+len(p.Changed)+len(p.Adjusted)+len(p.Gone) != 0 {
		t.Fatalf("preview = %s, want no items", jsonOf(t, p))
	}
	if !hasCode(p, "manual_name_taken") {
		t.Errorf("notes = %v, want manual_name_taken", noteCodes(p))
	}
	out := varsync.Apply([]schema.Field{mine}, p, varsync.ApplyOptions{RemoveGone: true})
	if !reflect.DeepEqual(out, []schema.Field{mine}) {
		t.Errorf("applied = %s, want the manual field untouched", jsonOf(t, out))
	}
}

func TestComputeSourceRefMoveIsUnchanged(t *testing.T) {
	old := marker("release", schema.TypeString, 3)
	cand := marker("release", schema.TypeString, 9, func(f *schema.Field) { f.SetInPlaybook = true })
	cur := stored(old, true)

	p := varsync.Compute([]schema.Field{cur}, source(cand), nil)

	if p.Unchanged != 1 || len(p.Changed) != 0 {
		t.Fatalf("unchanged %d changed %s", p.Unchanged, jsonOf(t, p.Changed))
	}
	for _, opt := range []varsync.ApplyOptions{{Automatic: true}, {}} {
		out := varsync.Apply([]schema.Field{cur}, p, opt)
		f := field(t, out, "release")
		if f.Source.Line != 9 || !f.SetInPlaybook || !*f.Active {
			t.Errorf("%+v: applied = %s, want line 9, set in playbook, still active", opt, jsonOf(t, f))
		}
	}
	if cur.Source.Line != 3 {
		t.Error("Apply changed the current field in place")
	}
}

func TestSubFieldsAreOneUnit(t *testing.T) {
	object := func(passType schema.Type) schema.Field {
		return marker("db", schema.TypeObject, 5, func(f *schema.Field) {
			f.Fields = []schema.Field{{Name: "user", Type: schema.TypeString}, {Name: "port", Type: passType}}
		})
	}

	t.Run("a source object's sub-field change is one change of fields", func(t *testing.T) {
		p := varsync.Compute([]schema.Field{stored(object(schema.TypeInt), true)}, source(object(schema.TypeString)), nil)
		if len(p.Changed) != 1 {
			t.Fatalf("changed = %s", jsonOf(t, p.Changed))
		}
		ch := p.Changed[0]
		if len(ch.Changes) != 1 || ch.Changes[0].Attr != "fields" {
			t.Errorf("changes = %s, want one change of fields", jsonOf(t, ch.Changes))
		}
	})
	t.Run("an adjusted object keeps its manual sub-field", func(t *testing.T) {
		mine := object(schema.TypeInt)
		mine.Fields = append(mine.Fields, schema.Field{Name: "mine", Type: schema.TypeBool})
		cur := adjusted(stored(mine, true), object(schema.TypeInt))
		p := varsync.Compute([]schema.Field{cur}, source(object(schema.TypeString)), nil)
		if len(p.Adjusted) != 1 || len(p.Changed) != 0 {
			t.Fatalf("adjusted %s changed %s", jsonOf(t, p.Adjusted), jsonOf(t, p.Changed))
		}
		if a := p.Adjusted[0]; len(a.Changes) != 1 || a.Changes[0].Attr != "fields" {
			t.Errorf("changes = %s", jsonOf(t, a.Changes))
		}
		out := varsync.Apply([]schema.Field{cur}, p, varsync.ApplyOptions{Automatic: true})
		f := field(t, out, "db")
		if got := names(f.Fields, func(s schema.Field) string { return s.Name }); !reflect.DeepEqual(got, []string{"user", "port", "mine"}) {
			t.Errorf("sub-fields = %v", got)
		}
		if f.Fields[1].Type != schema.TypeInt || f.SourceField.Fields[1].Type != schema.TypeString {
			t.Errorf("applied = %s, want the adjustment kept and the new source version recorded", jsonOf(t, f))
		}
	})
}

func TestComputeDiffAttributes(t *testing.T) {
	old := marker("x", schema.TypeString, 5, func(f *schema.Field) {
		f.Label, f.Description, f.Min, f.Pattern = "X", "old", ptr(1.0), "^a"
	})
	cand := marker("x", schema.TypeString, 5, func(f *schema.Field) {
		f.Label, f.Required, f.Max, f.Pattern = "Y", true, ptr(9.0), "^b"
	})
	p := varsync.Compute([]schema.Field{stored(old, true)}, source(cand), nil)
	if len(p.Changed) != 1 {
		t.Fatalf("changed = %s", jsonOf(t, p.Changed))
	}
	want := `[{"attr":"label","old":"X","new":"Y"},{"attr":"description","old":"old"},{"attr":"required","old":false,"new":true},` +
		`{"attr":"min","old":1},{"attr":"max","new":9},{"attr":"pattern","old":"^a","new":"^b"}]`
	if got := jsonOf(t, p.Changed[0].Changes); got != want {
		t.Errorf("changes =\n%s\nwant\n%s", got, want)
	}
}

func TestComputeUnavailableSource(t *testing.T) {
	cur := stored(marker("release", schema.TypeString, 3), true)
	src := source(marker("other", schema.TypeString, 4))
	src.Available, src.Reason = false, "the playbook could not be read"
	src.Playbook.Notes = []varsource.Note{{Level: varsource.LevelWarning, Code: "invalid_yaml", File: playbookPath, Message: "not valid YAML"}}

	p := varsync.Compute([]schema.Field{cur}, src, nil)

	if len(p.Added)+len(p.Changed)+len(p.Adjusted)+len(p.Gone)+p.Unchanged != 0 {
		t.Errorf("preview = %s, want no items", jsonOf(t, p))
	}
	if got, want := noteCodes(p), []string{"source_unavailable", "invalid_yaml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("notes = %v, want %v", got, want)
	}
	if !strings.Contains(p.Notes[0].Message, "could not be read") {
		t.Errorf("source_unavailable message = %q", p.Notes[0].Message)
	}
	if got := jsonOf(t, varsync.Apply([]schema.Field{cur}, p, varsync.ApplyOptions{RemoveGone: true})); got != jsonOf(t, []schema.Field{cur}) {
		t.Errorf("applied = %s", got)
	}
	if n := varsync.Compute([]schema.Field{cur}, varsync.Source{}, nil).Notes; len(n) != 1 || !strings.Contains(n[0].Message, "not available") {
		t.Errorf("notes without a reason = %+v", n)
	}
}

func TestComputeEmptyListsEncodeAsArrays(t *testing.T) {
	got := jsonOf(t, varsync.Compute(nil, source(), nil))
	want := `{"source_rev":"c1","added":[],"changed":[],"adjusted":[],"gone":[],"unchanged":0,"left_out":0,"notes":[]}`
	if got != want {
		t.Errorf("preview = %s, want %s", got, want)
	}
}
