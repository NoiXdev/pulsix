package varsource

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// noteKey renders a note as "level code file:line" so a test can compare
// the whole note list at once.
func noteKey(n Note) string {
	return fmt.Sprintf("%s %s %s:%d", n.Level, n.Code, n.File, n.Line)
}

// wantNotes fails unless got holds exactly the want notes, in order.
func wantNotes(t *testing.T, got []Note, want ...string) {
	t.Helper()
	keys := make([]string, len(got))
	for i, n := range got {
		keys[i] = noteKey(n)
		if n.Message == "" {
			t.Errorf("note %s has no message", keys[i])
		}
	}
	if len(want) == 0 {
		want = []string{}
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("notes = %q, want %q", keys, want)
	}
}

// wantFields fails unless got equals want field by field (deep, so an int
// default does not equal a float one).
func wantFields(t *testing.T, got, want []schema.Field) {
	t.Helper()
	if len(want) == 0 {
		want = nil
	}
	if len(got) == 0 {
		got = nil
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("fields (compared deeply: an int default is not a float one) =\n%s\nwant\n%s", g, w)
	}
}

const specFile = "roles/nginx/meta/argument_specs.yml"

// specYAML wraps option lines into an argument spec with entry point main;
// the first option line is line 4.
func specYAML(lines ...string) []byte {
	var b strings.Builder
	b.WriteString("argument_specs:\n  main:\n    options:\n")
	for _, l := range lines {
		b.WriteString("      " + l + "\n")
	}
	return []byte(b.String())
}

// specSource is the source of a field read from specFile's entry point main.
func specSource(line int) *schema.FieldSource {
	return &schema.FieldSource{Kind: schema.SourceArgumentSpec, Role: "nginx", EntryPoint: "main", File: specFile, Line: line}
}

// fromSpec is a top-level field read from specFile at line.
func fromSpec(line int, f schema.Field) schema.Field {
	f.Origin = schema.OriginSource
	f.Source = specSource(line)
	return f
}

func readSpec(t *testing.T, data []byte) ([]schema.Field, []Note) {
	t.Helper()
	fields, notes, readOK, entryFound := argumentSpecFields(data, specFile, "nginx", "", schema.SourceArgumentSpec)
	if !readOK || !entryFound {
		t.Fatalf("readOK = %v, entryFound = %v, want both true (notes %v)", readOK, entryFound, notes)
	}
	return fields, notes
}

func TestFieldLimits(t *testing.T) {
	choices := make([]string, 101)
	for i := range choices {
		choices[i] = fmt.Sprintf("c%d", i)
	}
	manyOptions := []string{"wide:", "  type: dict", "  options:"}
	for i := range 101 {
		manyOptions = append(manyOptions, fmt.Sprintf("    o%03d: {type: str}", i))
	}
	var hundred []schema.Field
	for i := range 100 {
		hundred = append(hundred, schema.Field{Name: fmt.Sprintf("o%03d", i), Type: schema.TypeString})
	}
	var topLevel []string
	var topWant []schema.Field
	for i := range 101 {
		topLevel = append(topLevel, fmt.Sprintf("t%03d: {type: str}", i))
		if i < 100 {
			topWant = append(topWant, fromSpec(4+i, schema.Field{Name: fmt.Sprintf("t%03d", i), Type: schema.TypeString}))
		}
	}
	cases := []struct {
		name   string
		spec   []byte
		fields []schema.Field
		notes  []string
	}{
		{"uppercase name", specYAML("Port: {type: int}"), nil,
			[]string{"warning invalid_name " + specFile + ":4"}},
		{"reserved prefix", specYAML("ansible_port: {type: int}"), nil,
			[]string{"warning invalid_name " + specFile + ":4"}},
		{"invalid sub-field name", specYAML(
			"db:",
			"  type: dict",
			"  options:",
			"    Bad: {type: str}",
			"    ansible_ok: {type: str}",
		), []schema.Field{fromSpec(4, schema.Field{Name: "db", Type: schema.TypeObject, Fields: []schema.Field{
			{Name: "ansible_ok", Type: schema.TypeString},
		}})}, []string{"warning invalid_name " + specFile + ":4"}},
		{"five nested dicts", specYAML(
			"l1:",
			"  type: dict",
			"  options:",
			"    l2:",
			"      type: dict",
			"      options:",
			"        l3:",
			"          type: dict",
			"          options:",
			"            l4:",
			"              type: dict",
			"              options:",
			"                keep: {type: str}",
			"                l5:",
			"                  type: dict",
			"                  options:",
			"                    leaf: {type: str}",
		), []schema.Field{fromSpec(4, schema.Field{Name: "l1", Type: schema.TypeObject, Fields: []schema.Field{
			{Name: "l2", Type: schema.TypeObject, Fields: []schema.Field{
				{Name: "l3", Type: schema.TypeObject, Fields: []schema.Field{
					{Name: "l4", Type: schema.TypeObject, Fields: []schema.Field{
						{Name: "keep", Type: schema.TypeString},
					}},
				}},
			}},
		}})}, []string{"warning too_deep " + specFile + ":4"}},
		{"101 choices", specYAML("mode: {choices: [" + strings.Join(choices, ", ") + "]}"), nil,
			[]string{"warning too_many_options " + specFile + ":4"}},
		{"101 options in one dict", specYAML(manyOptions...),
			[]schema.Field{fromSpec(4, schema.Field{Name: "wide", Type: schema.TypeObject, Fields: hundred})},
			[]string{"warning too_many_fields " + specFile + ":4"}},
		{"101 top-level options", specYAML(topLevel...), topWant,
			[]string{"warning too_many_fields " + specFile + ":104"}},
		{"every sub-field skipped", specYAML(
			"db:",
			"  type: dict",
			"  options:",
			"    Bad: {type: str}",
		), nil, []string{
			"warning invalid_name " + specFile + ":4",
			"warning field_invalid " + specFile + ":4",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields, notes := readSpec(t, tc.spec)
			wantFields(t, fields, tc.fields)
			wantNotes(t, notes, tc.notes...)
		})
	}
}

func TestFinishFieldChecksTheDefault(t *testing.T) {
	src := &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: 7}
	field := func(typ schema.Type, def any) schema.Field {
		return schema.Field{Name: "x", Type: typ, Default: def, Origin: schema.OriginSource, Source: src}
	}
	cases := []struct {
		name  string
		in    schema.Field
		want  schema.Field
		ok    bool
		notes []string
	}{
		{"valid default kept", field(schema.TypeInt, 80), field(schema.TypeInt, 80), true, nil},
		{"wrong type dropped", field(schema.TypeInt, "80"), field(schema.TypeInt, nil), true,
			[]string{"warning default_invalid site.yml:7"}},
		{"not finite dropped", field(schema.TypeFloat, math.Inf(1)), field(schema.TypeFloat, nil), true,
			[]string{"warning default_invalid site.yml:7"}},
		{"templated string dropped", field(schema.TypeString, "{{ x }}"), field(schema.TypeString, nil), true,
			[]string{"hint templated_default site.yml:7"}},
		{"templated list item dropped", field(schema.TypeList, []any{"a", "{% if x %}b{% endif %}"}),
			func() schema.Field {
				f := field(schema.TypeList, nil)
				f.Item = &schema.Field{Type: schema.TypeString}
				return f
			}(), true, []string{"hint templated_default site.yml:7"}},
		{"sub-field default dropped", schema.Field{Name: "db", Type: schema.TypeObject, Origin: schema.OriginSource, Source: src,
			Fields: []schema.Field{{Name: "port", Type: schema.TypeInt, Default: "x"}}},
			schema.Field{Name: "db", Type: schema.TypeObject, Origin: schema.OriginSource, Source: src,
				Fields: []schema.Field{{Name: "port", Type: schema.TypeInt}}},
			true, []string{"warning default_invalid site.yml:7"}},
		{"a default problem beside another skips the field only", schema.Field{Name: "db", Type: schema.TypeObject, Origin: schema.OriginSource, Source: src,
			Fields: []schema.Field{{Name: "port", Type: schema.TypeInt, Default: "x"}, {Name: "user", Type: schema.TypeString, Pattern: "("}}},
			schema.Field{}, false, []string{"warning field_invalid site.yml:7"}},
		{"other problem skips the field", schema.Field{Name: "x", Type: schema.TypeString, Pattern: "(", Origin: schema.OriginSource, Source: src},
			schema.Field{}, false, []string{"warning field_invalid site.yml:7"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			if in.Type == schema.TypeList && in.Item == nil {
				in.Item = &schema.Field{Type: schema.TypeString}
			}
			got, notes, ok := finishField(in, "site.yml", 7)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (notes %v)", ok, tc.ok, notes)
			}
			if ok {
				wantFields(t, []schema.Field{got}, []schema.Field{tc.want})
			}
			wantNotes(t, notes, tc.notes...)
		})
	}
}
