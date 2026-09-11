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
	want := map[string]any{"vhosts": []any{
		map[string]any{"domain": "a.io", "ssl": true, "basic_auth": map[string]any{"user": "u"}},
		"malformed",
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
