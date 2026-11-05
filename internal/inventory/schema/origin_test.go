package schema_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func roleSource() *schema.FieldSource {
	return &schema.FieldSource{Kind: schema.SourceArgumentSpec, Role: "nginx", EntryPoint: "main", File: "roles/nginx/meta/argument_specs.yml", Line: 12}
}

func TestCheckOriginAttributes(t *testing.T) {
	port := func(mod func(*schema.Field)) schema.Field {
		f := schema.Field{Name: "nginx_port", Type: schema.TypeInt, Default: 80.0}
		mod(&f)
		return f
	}
	object := func(sub func(*schema.Field)) schema.Field {
		s := schema.Field{Name: "user", Type: schema.TypeString}
		sub(&s)
		return schema.Field{Name: "db", Type: schema.TypeObject, Fields: []schema.Field{s}}
	}
	cases := []struct {
		name  string
		field schema.Field
		path  string // "" means the schema passes Check
		msg   string
	}{
		{"unknown origin", port(func(f *schema.Field) { f.Origin = "elsewhere" }), "fields[0].origin", "unknown origin"},
		{"source origin without a source", port(func(f *schema.Field) { f.Origin = schema.OriginSource }), "fields[0].source", "needs a source"},
		{"manual field with a source", port(func(f *schema.Field) { f.Source = roleSource() }), "fields[0].source", "manual"},
		{"explicit manual field with a source", port(func(f *schema.Field) { f.Origin = schema.OriginManual; f.Source = roleSource() }), "fields[0].source", "manual"},
		{"unknown source kind", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginSource, &schema.FieldSource{Kind: "x", File: "site.yml"}
		}), "fields[0].source.kind", "unknown source kind"},
		{"source without a file", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginSource, &schema.FieldSource{Kind: schema.SourceVarsPrompt}
		}), "fields[0].source.file", "needs a file"},
		{"negative source line", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginSource, &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: -1}
		}), "fields[0].source.line", "≥ 0"},
		{"sub-field active", object(func(s *schema.Field) { s.Active = ptr(false) }), "fields[0].fields[0].active", "only top-level fields"},
		{"sub-field origin", object(func(s *schema.Field) { s.Origin = schema.OriginManual }), "fields[0].fields[0].origin", "only top-level fields"},
		{"sub-field source", object(func(s *schema.Field) { s.Source = roleSource() }), "fields[0].fields[0].source", "only top-level fields"},
		{"sub-field set_in_playbook", object(func(s *schema.Field) { s.SetInPlaybook = true }), "fields[0].fields[0].set_in_playbook", "only top-level fields"},
		{"sub-field source_field", object(func(s *schema.Field) {
			s.SourceField = &schema.Field{Name: "user", Type: schema.TypeString}
		}), "fields[0].fields[0].source_field", "only top-level fields"},
		{"list item origin", schema.Field{Name: "tags", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString, Origin: schema.OriginSource}},
			"fields[0].item.origin", "only top-level fields"},
		{"source_field on a source field", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginSource, roleSource()
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeInt}
		}), "fields[0].source_field", "adjusted"},
		{"source_field on a manual field", port(func(f *schema.Field) {
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeInt}
		}), "fields[0].source_field", "adjusted"},
		{"source_field with another name", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginAdjusted, roleSource()
			f.SourceField = &schema.Field{Name: "other_port", Type: schema.TypeInt}
		}), "fields[0].source_field.name", "field's own name"},
		{"source_field failing its own check", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginAdjusted, roleSource()
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeEnum}
		}), "fields[0].source_field.options", "at least one option"},
		{"source_field carrying an origin", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginAdjusted, roleSource()
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeInt, Origin: schema.OriginSource}
		}), "fields[0].source_field.origin", "source's version"},
		{"adjusted field with a valid source_field", port(func(f *schema.Field) {
			f.Origin, f.Source, f.Label = schema.OriginAdjusted, roleSource(), "Port"
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeInt, Default: 8080.0}
		}), "", ""},
		{"gone field keeps its source_field", port(func(f *schema.Field) {
			f.Origin, f.Source = schema.OriginGone, roleSource()
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeInt}
		}), "", ""},
		{"inactive source field set in the playbook", port(func(f *schema.Field) {
			f.Origin, f.Source, f.Active, f.SetInPlaybook = schema.OriginSource, roleSource(), ptr(false), true
		}), "", ""},
		{"inactive manual field", port(func(f *schema.Field) { f.Active = ptr(false) }), "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := schema.Schema{Fields: []schema.Field{c.field}}.Check()
			if c.path == "" {
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				return
			}
			ps := problemsOf(t, err)
			if !hasProblem(ps, c.path, c.msg) {
				t.Fatalf("missing %q at %q in %+v", c.msg, c.path, ps)
			}
		})
	}
}

func TestCheckManualOnly(t *testing.T) {
	for _, o := range []schema.Origin{schema.OriginSource, schema.OriginAdjusted, schema.OriginGone} {
		t.Run(string(o), func(t *testing.T) {
			s := schema.Schema{Fields: []schema.Field{
				{Name: "a", Type: schema.TypeString},
				{Name: "b", Type: schema.TypeString, Origin: o, Source: roleSource()},
			}}
			ps := problemsOf(t, s.CheckManualOnly())
			if len(ps) != 1 || !hasProblem(ps, "fields[1].origin", "only job templates take fields from a playbook") {
				t.Fatalf("problems = %+v", ps)
			}
		})
	}
	t.Run("set in playbook", func(t *testing.T) {
		s := schema.Schema{Fields: []schema.Field{{Name: "a", Type: schema.TypeString, SetInPlaybook: true}}}
		if ps := problemsOf(t, s.CheckManualOnly()); !hasProblem(ps, "fields[0].set_in_playbook", "only job templates take fields from a playbook") {
			t.Fatalf("problems = %+v", ps)
		}
	})
	manual := schema.Schema{Fields: []schema.Field{
		{Name: "a", Type: schema.TypeString},
		{Name: "b", Type: schema.TypeString, Origin: schema.OriginManual, Active: ptr(false)},
	}}
	if err := manual.CheckManualOnly(); err != nil {
		t.Fatalf("manual fields: %v", err)
	}
	if err := (schema.Schema{}).CheckManualOnly(); err != nil {
		t.Fatalf("empty schema: %v", err)
	}
}

func TestFieldJSONKeepsActiveFalse(t *testing.T) {
	b, err := json.Marshal(schema.Field{Name: "a", Type: schema.TypeString, Active: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"active":false`) {
		t.Fatalf("json = %s, want \"active\":false", b)
	}
	var f schema.Field
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.IsActive() {
		t.Fatal("a field stored inactive must read back inactive")
	}

	var legacy schema.Field
	if err := json.Unmarshal([]byte(`{"name":"a","type":"string"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.IsActive() || legacy.EffectiveOrigin() != schema.OriginManual {
		t.Fatalf("a field without active and origin must be an active manual field: active %v, origin %q", legacy.IsActive(), legacy.EffectiveOrigin())
	}
	b, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"a","type":"string"}` {
		t.Fatalf("a legacy field must round-trip unchanged: %s", b)
	}
	if got := (schema.Field{Origin: schema.OriginGone}).EffectiveOrigin(); got != schema.OriginGone {
		t.Fatalf("EffectiveOrigin = %q, want gone", got)
	}
}

func TestSameContent(t *testing.T) {
	base := schema.Field{Name: "nginx_port", Label: "Port", Type: schema.TypeInt, Default: 8080}
	cases := []struct {
		name string
		mod  func(*schema.Field)
		want bool
	}{
		{"int default against a decoded float default", func(f *schema.Field) { f.Default = float64(8080) }, true},
		{"label differs", func(f *schema.Field) { f.Label = "HTTP port" }, false},
		{"default differs", func(f *schema.Field) { f.Default = 80.0 }, false},
		{"only provenance differs", func(f *schema.Field) {
			f.Active, f.Origin, f.SetInPlaybook = ptr(false), schema.OriginSource, true
			f.Source = roleSource()
			f.Source.Line = 40
			f.SourceField = &schema.Field{Name: "nginx_port", Type: schema.TypeString}
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := base
			g.Source = roleSource()
			c.mod(&g)
			if got := base.SameContent(g); got != c.want {
				t.Fatalf("SameContent = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSendable(t *testing.T) {
	fields := []schema.Field{
		{Name: "a", Type: schema.TypeString},
		{Name: "b", Type: schema.TypeString, Active: ptr(false)},
		{Name: "c", Type: schema.TypeString, Origin: schema.OriginGone, Source: roleSource(), Active: ptr(true)},
		{Name: "e", Type: schema.TypeString},
	}
	values := map[string]any{"a": "1", "b": "2", "c": "3", "d": "4"}
	got := schema.Sendable(fields, values)
	if want := map[string]any{"a": "1", "c": "3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Sendable = %#v, want %#v", got, want)
	}
	if got := schema.Sendable(fields, nil); got == nil || len(got) != 0 {
		t.Fatalf("Sendable(nil) = %#v, want an empty map", got)
	}
}
