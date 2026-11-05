package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func vhost(domain string, extra map[string]any) map[string]any {
	m := map[string]any{"domain": domain}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestValidate(t *testing.T) {
	host := specExample().HostFields()
	group := specExample().GroupFields()
	str := schema.Field{Name: "s", Type: schema.TypeString}
	list := schema.Field{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}, MaxItems: ptr(1)}
	cases := []struct {
		name     string
		fields   []schema.Field
		values   map[string]any
		problems map[string]string // path -> message substring; nil means valid
	}{
		{"valid", host, map[string]any{"vhosts": []any{vhost("acme.io", map[string]any{
			"php_version": "8.3", "aliases": []any{"www.acme.io"},
			"basic_auth": map[string]any{"user": "u", "password": "p"},
		})}}, nil},
		{"required missing", host, map[string]any{}, map[string]string{"vhosts": "is required"}},
		{"null counts as missing", host, map[string]any{"vhosts": nil}, map[string]string{"vhosts": "is required"}},
		{"too few items", host, map[string]any{"vhosts": []any{}}, map[string]string{"vhosts": "at least 1"}},
		{"nested required and pattern", host, map[string]any{"vhosts": []any{vhost("acme.io", nil), vhost("Bad Domain", nil), map[string]any{}}},
			map[string]string{"vhosts[1].domain": "must match", "vhosts[2].domain": "is required"}},
		{"unknown nested key", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"extra": 1.0})}}, map[string]string{"vhosts[0].extra": "unknown field"}},
		{"unknown top key", host, map[string]any{"vhosts": []any{vhost("a.io", nil)}, "nope": true}, map[string]string{"nope": "unknown field"}},
		{"enum", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"php_version": "7.4"})}}, map[string]string{"vhosts[0].php_version": "one of 8.2, 8.3, 8.4"}},
		{"list item type", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"aliases": []any{"ok", 3.0}})}}, map[string]string{"vhosts[0].aliases[1]": "must be a string"}},
		{"null list item", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"aliases": []any{nil}})}}, map[string]string{"vhosts[0].aliases[0]": "must not be empty"}},
		{"repeater item not an object", host, map[string]any{"vhosts": []any{"a.io"}}, map[string]string{"vhosts[0]": "entry"}},
		{"repeater must be a list", host, map[string]any{"vhosts": "a.io"}, map[string]string{"vhosts": "list of entries"}},
		{"secret empty", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"basic_auth": map[string]any{"password": ""}})}}, map[string]string{"vhosts[0].basic_auth.password": "must not be empty"}},
		{"secret wrong type", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"basic_auth": map[string]any{"password": 5.0}})}}, map[string]string{"vhosts[0].basic_auth.password": "must be a string"}},
		{"sealed secret accepted", host, map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"basic_auth": map[string]any{"password": schema.Sealed([]byte{1, 2, 3})}})}}, nil},
		{"int fractional", group, map[string]any{"max_upload_mb": 1.5}, map[string]string{"max_upload_mb": "whole number"}},
		{"int range", group, map[string]any{"max_upload_mb": 4096.0}, map[string]string{"max_upload_mb": "at most 2048"}},
		{"int below min", group, map[string]any{"max_upload_mb": 0.0}, map[string]string{"max_upload_mb": "at least 1"}},
		{"int from json.Number", group, map[string]any{"max_upload_mb": json.Number("10")}, nil},
		{"int from Go int", group, map[string]any{"max_upload_mb": 10}, nil},
		{"int must be a number", group, map[string]any{"max_upload_mb": "10"}, map[string]string{"max_upload_mb": "whole number"}},
		{"bool", []schema.Field{{Name: "b", Type: schema.TypeBool}}, map[string]any{"b": "yes"}, map[string]string{"b": "true or false"}},
		{"float", []schema.Field{{Name: "f", Type: schema.TypeFloat, Min: ptr(0.5)}}, map[string]any{"f": 0.25}, map[string]string{"f": "at least 0.5"}},
		{"float above max", []schema.Field{{Name: "f", Type: schema.TypeFloat, Max: ptr(1.0)}}, map[string]any{"f": 1.5}, map[string]string{"f": "at most 1"}},
		{"float must be a number", []schema.Field{{Name: "f", Type: schema.TypeFloat}}, map[string]any{"f": true}, map[string]string{"f": "must be a number"}},
		{"string length counts characters", []schema.Field{{Name: "s", Type: schema.TypeString, Max: ptr(3.0)}}, map[string]any{"s": "äöü"}, nil},
		{"string too long", []schema.Field{{Name: "s", Type: schema.TypeString, Max: ptr(3.0)}}, map[string]any{"s": "äöüß"}, map[string]string{"s": "at most 3 characters"}},
		{"string too short", []schema.Field{{Name: "s", Type: schema.TypeString, Min: ptr(2.0)}}, map[string]any{"s": "a"}, map[string]string{"s": "at least 2 characters"}},
		{"string wrong type", []schema.Field{str}, map[string]any{"s": 1.0}, map[string]string{"s": "must be a string"}},
		{"object must be an object", []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{str}}}, map[string]any{"o": "x"}, map[string]string{"o": "must be an object"}},
		{"required with default is satisfied", []schema.Field{{Name: "d", Type: schema.TypeString, Required: true, Default: "x"}}, map[string]any{}, nil},
		{"list must be a list", []schema.Field{list}, map[string]any{"l": "x"}, map[string]string{"l": "must be a list"}},
		{"max items", []schema.Field{list}, map[string]any{"l": []any{"a", "b"}}, map[string]string{"l": "at most 1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := schema.Validate(c.fields, c.values)
			if c.problems == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			ps := problemsOf(t, err)
			if len(ps) != len(c.problems) {
				t.Fatalf("problems = %+v, want %d", ps, len(c.problems))
			}
			for path, msg := range c.problems {
				if !hasProblem(ps, path, msg) {
					t.Errorf("missing %q at %q in %+v", msg, path, ps)
				}
			}
		})
	}
}

func TestValidateStoredIgnoresUndeclaredKeys(t *testing.T) {
	host := specExample().HostFields()
	values := map[string]any{"vhosts": []any{vhost("a.io", map[string]any{"removed_sub_field": 1.0})}, "removed_field": "x"}
	if err := schema.ValidateStored(host, values); err != nil {
		t.Fatalf("ValidateStored: %v", err)
	}
	if err := schema.Validate(host, values); err == nil {
		t.Fatal("Validate must reject the same values")
	}
	if err := schema.ValidateStored(host, map[string]any{}); err == nil {
		t.Fatal("ValidateStored must still report missing required values")
	}
}

func TestValidationErrorMessage(t *testing.T) {
	e := &schema.ValidationError{Problems: []schema.Problem{{Path: "", Message: "x"}, {Path: "a", Message: "y"}}}
	if got := e.Error(); got != "invalid: (root): x; a: y" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestValidateIgnoresInactiveFields is spec goal 3: a value under an
// inactive field is neither type-checked nor reported as undeclared, and
// "required" applies to active fields only.
func TestValidateIgnoresInactiveFields(t *testing.T) {
	fields := []schema.Field{
		{Name: "port", Type: schema.TypeInt, Required: true, Active: ptr(false)},
		{Name: "release", Type: schema.TypeString, Required: true},
	}
	cases := []struct {
		name     string
		values   map[string]any
		problems map[string]string // path -> message substring; nil means valid
		strict   bool              // the case applies to Validate only
	}{
		{"wrong type under an inactive field", map[string]any{"port": "x", "release": "v1"}, nil, false},
		{"inactive required field missing", map[string]any{"release": "v1"}, nil, false},
		{"active required field missing", map[string]any{"port": "x"}, map[string]string{"release": "is required"}, false},
		{"undeclared key", map[string]any{"release": "v1", "nope": 1.0}, map[string]string{"nope": "unknown field"}, true},
	}
	validators := []struct {
		name string
		fn   func([]schema.Field, map[string]any) error
	}{{"Validate", schema.Validate}, {"ValidateStored", schema.ValidateStored}}
	for _, v := range validators {
		for _, c := range cases {
			if c.strict && v.name != "Validate" {
				continue
			}
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				err := v.fn(fields, c.values)
				if c.problems == nil {
					if err != nil {
						t.Fatalf("%s: %v", v.name, err)
					}
					return
				}
				ps := problemsOf(t, err)
				if len(ps) != len(c.problems) {
					t.Fatalf("problems = %+v, want %d", ps, len(c.problems))
				}
				for path, msg := range c.problems {
					if !hasProblem(ps, path, msg) {
						t.Errorf("missing %q at %q in %+v", msg, path, ps)
					}
				}
			})
		}
	}
	if err := schema.ValidateStored(fields, map[string]any{"release": "v1", "nope": 1.0}); err != nil {
		t.Fatalf("ValidateStored still ignores undeclared keys: %v", err)
	}
}
