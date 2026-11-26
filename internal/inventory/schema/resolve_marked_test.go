package schema_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// wrapped is the test's stand-in for the renderer's inert form: whatever
// passes through it is recognisable in the result.
func wrapped(v any) any { return map[string]any{"wrapped": v} }

// TestResolveMarkedWrapsAssistantValuesAndDefaults pins decision D6 (spec
// 2026-11-25 assistant API gaps): a value an assistant wrote, and a schema
// default an assistant set where it fills in, pass through wrap (the
// renderer's inert form); everything a person wrote resolves as Resolve
// does. The second result names the fields that hold a wrapped part.
func TestResolveMarkedWrapsAssistantValuesAndDefaults(t *testing.T) {
	fields := []schema.Field{
		{Name: "greeting", Type: schema.TypeString},
		{Name: "php", Type: schema.TypeString, Default: "8.3"},
		{Name: "upload", Type: schema.TypeInt, Default: 64.0},
		{Name: "site", Type: schema.TypeObject, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString},
			{Name: "root", Type: schema.TypeString, Default: "/srv/{{ x }}"},
		}},
		{Name: "plain", Type: schema.TypeString},
	}
	values := map[string]any{
		"greeting": "{{ lookup('pipe','id') }}",
		"upload":   128.0,
		"site":     map[string]any{"domain": "a.io"},
		"plain":    "person",
	}
	cases := []struct {
		name   string
		marks  schema.Marks
		want   map[string]any
		marked []string
	}{
		{"no marks resolve as Resolve", schema.Marks{}, schema.Resolve(fields, values), nil},
		{
			"a marked value is wrapped whole",
			schema.Marks{Values: map[string]bool{"greeting": true}},
			map[string]any{"greeting": wrapped("{{ lookup('pipe','id') }}"), "php": "8.3", "upload": 128.0,
				"site": map[string]any{"domain": "a.io", "root": "/srv/{{ x }}"}, "plain": "person"},
			[]string{"greeting"},
		},
		{
			"a marked default is wrapped where it fills in, not where a value wins",
			schema.Marks{Defaults: map[string]bool{"php": true, "upload": true, "site": true}},
			map[string]any{"greeting": "{{ lookup('pipe','id') }}", "php": wrapped("8.3"), "upload": 128.0,
				"site": map[string]any{"domain": "a.io", "root": wrapped("/srv/{{ x }}")}, "plain": "person"},
			[]string{"php", "site"},
		},
		{
			"a marked value of an object wraps its nested defaults too",
			schema.Marks{Values: map[string]bool{"site": true}},
			map[string]any{"greeting": "{{ lookup('pipe','id') }}", "php": "8.3", "upload": 128.0,
				"site": wrapped(map[string]any{"domain": "a.io", "root": "/srv/{{ x }}"}), "plain": "person"},
			[]string{"site"},
		},
		{
			"a marked value is wrapped once, its marked nested defaults with it",
			schema.Marks{Values: map[string]bool{"site": true}, Defaults: map[string]bool{"site": true}},
			map[string]any{"greeting": "{{ lookup('pipe','id') }}", "php": "8.3", "upload": 128.0,
				"site": wrapped(map[string]any{"domain": "a.io", "root": "/srv/{{ x }}"}), "plain": "person"},
			[]string{"site"},
		},
	}
	for _, c := range cases {
		got, marked := schema.ResolveMarked(fields, values, c.marks, wrapped)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ResolveMarked =\n%#v\nwant\n%#v", c.name, got, c.want)
		}
		if !slices.Equal(marked, c.marked) {
			t.Errorf("%s: marked = %v, want %v", c.name, marked, c.marked)
		}
	}
}

// TestResolveMarkedSkipsInactiveAndMisshapedValues: a marked value whose
// shape no longer fits falls back to the default (wrapped only when the
// default is marked), and an inactive field stays out entirely.
func TestResolveMarkedSkipsInactiveAndMisshapedValues(t *testing.T) {
	off := false
	fields := []schema.Field{
		{Name: "n", Type: schema.TypeInt, Default: 1.0},
		{Name: "gone", Type: schema.TypeString, Active: &off},
	}
	got, marked := schema.ResolveMarked(fields, map[string]any{"n": "not a number", "gone": "x"},
		schema.Marks{Values: map[string]bool{"n": true, "gone": true}}, wrapped)
	if !reflect.DeepEqual(got, map[string]any{"n": 1.0}) || marked != nil {
		t.Fatalf("ResolveMarked = %#v, %v", got, marked)
	}
	got, marked = schema.ResolveMarked(fields, map[string]any{"n": "not a number"},
		schema.Marks{Values: map[string]bool{"n": true}, Defaults: map[string]bool{"n": true}}, wrapped)
	if !reflect.DeepEqual(got, map[string]any{"n": wrapped(1.0)}) || !slices.Equal(marked, []string{"n"}) {
		t.Fatalf("ResolveMarked, marked default behind a misshaped marked value = %#v, %v", got, marked)
	}
}
