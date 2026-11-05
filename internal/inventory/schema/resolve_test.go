package schema_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func TestResolveFillsDefaultsAndDropsUndeclaredKeys(t *testing.T) {
	s := specExample()
	got := schema.Resolve(s.HostFields(), map[string]any{
		"vhosts": []any{
			vhost("a.io", map[string]any{"stale": 1.0, "basic_auth": map[string]any{"user": "u", "old": true}}),
			"malformed",
		},
		"gone": true,
	})
	// A repeater entry that is not an object is left out (see
	// TestResolveDropsShapeMismatches); it used to be passed through.
	want := map[string]any{"vhosts": []any{
		map[string]any{"domain": "a.io", "ssl": true, "basic_auth": map[string]any{"user": "u"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve(host) = %#v", got)
	}
	if got := schema.Resolve(s.GroupFields(), nil); !reflect.DeepEqual(got, map[string]any{"php_default": "8.3", "max_upload_mb": 64.0}) {
		t.Fatalf("Resolve(group) = %#v", got)
	}
	if got := schema.Resolve(s.GroupFields(), map[string]any{"php_default": "8.4", "max_upload_mb": nil}); got["php_default"] != "8.4" || got["max_upload_mb"] != 64.0 {
		t.Fatalf("explicit value and null: %#v", got)
	}
}

func TestOrphans(t *testing.T) {
	fields := specExample().GroupFields()
	if got := schema.Orphans(fields, map[string]any{"b": 1, "a": 2, "php_default": "8.3"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Orphans = %v", got)
	}
	if got := schema.Orphans(fields, map[string]any{"php_default": "8.3"}); got != nil {
		t.Fatalf("Orphans = %v, want nil", got)
	}
}

// TestResolveDropsShapeMismatches pins that a stored value whose shape no
// longer matches its field — after a schema type change, say — never reaches
// Ansible: it is treated as absent (so a schema default applies), list and
// repeater entries of the wrong shape are left out, and nothing else changes.
func TestResolveDropsShapeMismatches(t *testing.T) {
	sealed := schema.Sealed([]byte("ciphertext"))
	str := schema.Field{Name: "v", Type: schema.TypeString}
	cases := []struct {
		name  string
		field schema.Field
		value any
		want  map[string]any
	}{
		{"sealed value under a string", str, sealed, map[string]any{}},
		{"map under a string falls back to the default", schema.Field{Name: "v", Type: schema.TypeString, Default: "d"}, map[string]any{"a": 1.0}, map[string]any{"v": "d"}},
		{"list under a string", str, []any{"a"}, map[string]any{}},
		{"string kept", str, "ok", map[string]any{"v": "ok"}},
		{"map under an enum", schema.Field{Name: "v", Type: schema.TypeEnum, Options: []string{"a"}}, sealed, map[string]any{}},
		{"sealed value under an int", schema.Field{Name: "v", Type: schema.TypeInt}, sealed, map[string]any{}},
		{"string under an int", schema.Field{Name: "v", Type: schema.TypeInt}, "12", map[string]any{}},
		{"number kept", schema.Field{Name: "v", Type: schema.TypeFloat}, 1.5, map[string]any{"v": 1.5}},
		{"string under a bool", schema.Field{Name: "v", Type: schema.TypeBool}, "true", map[string]any{}},
		{"bool kept", schema.Field{Name: "v", Type: schema.TypeBool}, false, map[string]any{"v": false}},
		{"map under a secret", schema.Field{Name: "v", Type: schema.TypeSecret}, map[string]any{"x": "y"}, map[string]any{}},
		{"sealed value under a list", schema.Field{Name: "v", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}}, sealed, map[string]any{}},
		{"list drops entries of the wrong shape", schema.Field{Name: "v", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}},
			[]any{"a", sealed, 2.0, []any{"x"}, "b"}, map[string]any{"v": []any{"a", "b"}}},
		{"string under a repeater", schema.Field{Name: "v", Type: schema.TypeRepeater, Fields: []schema.Field{str}}, "x", map[string]any{}},
		{"repeater drops entries that are not objects", schema.Field{Name: "v", Type: schema.TypeRepeater, Fields: []schema.Field{str}},
			[]any{map[string]any{"v": "a"}, "malformed", sealed, map[string]any{"v": sealed}}, map[string]any{"v": []any{map[string]any{"v": "a"}, map[string]any{}, map[string]any{}}}},
		{"list under an object", schema.Field{Name: "v", Type: schema.TypeObject, Fields: []schema.Field{str}}, []any{"x"}, map[string]any{}},
		{"object resolves its fields", schema.Field{Name: "v", Type: schema.TypeObject, Fields: []schema.Field{str}}, map[string]any{"v": sealed}, map[string]any{"v": map[string]any{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := schema.Resolve([]schema.Field{c.field}, map[string]any{"v": c.value})
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Resolve = %#v, want %#v", got, c.want)
			}
		})
	}
}

// TestResolveSkipsInactiveFields is plan decision 7: an inactive group
// field is left out of the rendered inventory, its stored value and its
// default alike.
func TestResolveSkipsInactiveFields(t *testing.T) {
	fields := []schema.Field{
		{Name: "port", Type: schema.TypeInt, Default: 80.0, Active: ptr(false)},
		{Name: "proxy", Type: schema.TypeString, Default: "none", Active: ptr(false)},
		{Name: "env", Type: schema.TypeString, Default: "prod"},
	}
	got := schema.Resolve(fields, map[string]any{"port": 8080.0})
	if want := map[string]any{"env": "prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve = %#v, want %#v", got, want)
	}
}
