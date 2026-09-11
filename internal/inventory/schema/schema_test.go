package schema_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func ptr[T any](v T) *T { return &v }

// specExample is spec §6's example schema (with min_items instead of the
// example's min on a repeater) plus two group-scoped fields.
func specExample() schema.Schema {
	return schema.Schema{Fields: []schema.Field{
		{
			Name: "vhosts", Label: "Virtual hosts", Type: schema.TypeRepeater, Scope: schema.ScopeHost,
			Required: true, MinItems: ptr(1), Description: "Virtual hosts served by this host",
			Fields: []schema.Field{
				{Name: "domain", Type: schema.TypeString, Required: true, Pattern: `^[a-z0-9.-]+$`},
				{Name: "php_version", Type: schema.TypeEnum, Options: []string{"8.2", "8.3", "8.4"}},
				{Name: "ssl", Type: schema.TypeBool, Default: true},
				{Name: "aliases", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}},
				{Name: "basic_auth", Type: schema.TypeObject, Fields: []schema.Field{
					{Name: "user", Type: schema.TypeString},
					{Name: "password", Type: schema.TypeSecret},
				}},
			},
		},
		{Name: "php_default", Type: schema.TypeEnum, Options: []string{"8.3", "8.4"}, Default: "8.3", Overridable: true},
		{Name: "max_upload_mb", Type: schema.TypeInt, Min: ptr(1.0), Max: ptr(2048.0), Default: 64.0},
	}}
}

// nest returns an object field with levels object levels, the innermost
// holding one string field.
func nest(levels int) schema.Field {
	f := schema.Field{Name: "leaf", Type: schema.TypeString}
	for i := 0; i < levels; i++ {
		f = schema.Field{Name: fmt.Sprintf("o%d", i), Type: schema.TypeObject, Fields: []schema.Field{f}}
	}
	return f
}

func problemsOf(t *testing.T, err error) []schema.Problem {
	t.Helper()
	var ve *schema.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *schema.ValidationError", err)
	}
	return ve.Problems
}

func hasProblem(ps []schema.Problem, path, substr string) bool {
	for _, p := range ps {
		if p.Path == path && strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

func TestCheckAcceptsSpecExample(t *testing.T) {
	if err := specExample().Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := (schema.Schema{}).Check(); err != nil {
		t.Fatalf("empty schema: %v", err)
	}
	if err := (schema.Schema{Fields: []schema.Field{nest(4)}}).Check(); err != nil {
		t.Fatalf("four levels: %v", err)
	}
}

func TestCheckRejects(t *testing.T) {
	str := schema.Field{Name: "s", Type: schema.TypeString}
	cases := []struct {
		name   string
		fields []schema.Field
		path   string
		msg    string
	}{
		{"bad name", []schema.Field{{Name: "Bad-Name", Type: schema.TypeString}}, "fields[0].name", "lowercase"},
		{"reserved prefix", []schema.Field{{Name: "ansible_user", Type: schema.TypeString}}, "fields[0].name", "ansible_"},
		{"duplicate", []schema.Field{str, str}, "fields[1].name", "duplicate"},
		{"unknown type", []schema.Field{{Name: "x", Type: "date"}}, "fields[0].type", "unknown type"},
		{"bad scope", []schema.Field{{Name: "x", Type: schema.TypeString, Scope: "zone"}}, "fields[0].scope", "group or host"},
		{"overridable host field", []schema.Field{{Name: "x", Type: schema.TypeString, Scope: schema.ScopeHost, Overridable: true}}, "fields[0].overridable", "group-scoped"},
		{"nested scope", []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{{Name: "x", Type: schema.TypeString, Scope: schema.ScopeHost}}}}, "fields[0].fields[0].scope", "top-level"},
		{"nested overridable", []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{{Name: "x", Type: schema.TypeString, Overridable: true}}}}, "fields[0].fields[0].overridable", "top-level"},
		{"enum without options", []schema.Field{{Name: "e", Type: schema.TypeEnum}}, "fields[0].options", "at least one option"},
		{"enum duplicate option", []schema.Field{{Name: "e", Type: schema.TypeEnum, Options: []string{"a", "a"}}}, "fields[0].options", "duplicate"},
		{"enum empty option", []schema.Field{{Name: "e", Type: schema.TypeEnum, Options: []string{""}}}, "fields[0].options", "empty"},
		{"options on string", []schema.Field{{Name: "s", Type: schema.TypeString, Options: []string{"a"}}}, "fields[0].options", "enum fields only"},
		{"min above max", []schema.Field{{Name: "n", Type: schema.TypeInt, Min: ptr(5.0), Max: ptr(1.0)}}, "fields[0].max", "less than min"},
		{"min on list", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}, Min: ptr(1.0)}}, "fields[0].min", "min_items"},
		{"fractional string length", []schema.Field{{Name: "s", Type: schema.TypeString, Min: ptr(1.5)}}, "fields[0].min", "whole numbers"},
		{"bad pattern", []schema.Field{{Name: "s", Type: schema.TypeString, Pattern: "("}}, "fields[0].pattern", "invalid regular expression"},
		{"long pattern", []schema.Field{{Name: "s", Type: schema.TypeString, Pattern: strings.Repeat("a", schema.MaxPatternLen+1)}}, "fields[0].pattern", "at most"},
		{"pattern on int", []schema.Field{{Name: "n", Type: schema.TypeInt, Pattern: "^1$"}}, "fields[0].pattern", "string fields only"},
		{"min_items on string", []schema.Field{{Name: "s", Type: schema.TypeString, MinItems: ptr(1)}}, "fields[0].min_items", "list and repeater"},
		{"negative min_items", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}, MinItems: ptr(-1)}}, "fields[0].min_items", "≥ 0"},
		{"min_items above max_items", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}, MinItems: ptr(3), MaxItems: ptr(1)}}, "fields[0].max_items", "less than min_items"},
		{"list without item", []schema.Field{{Name: "l", Type: schema.TypeList}}, "fields[0].item", "item type"},
		{"item on string", []schema.Field{{Name: "s", Type: schema.TypeString, Item: &schema.Field{Type: schema.TypeString}}}, "fields[0].item", "list fields only"},
		{"list of objects", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeObject}}}, "fields[0].item.type", "must be string"},
		{"named list item", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Name: "x", Type: schema.TypeString}}}, "fields[0].item.name", "no name"},
		{"required list item", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString, Required: true}}}, "fields[0].item", "only type and validation"},
		{"enum item without options", []schema.Field{{Name: "l", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeEnum}}}, "fields[0].item.options", "at least one option"},
		{"repeater without fields", []schema.Field{{Name: "r", Type: schema.TypeRepeater}}, "fields[0].fields", "at least one sub-field"},
		{"fields on string", []schema.Field{{Name: "s", Type: schema.TypeString, Fields: []schema.Field{str}}}, "fields[0].fields", "repeater and object"},
		{"nested duplicate", []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{str, str}}}, "fields[0].fields[1].name", "duplicate"},
		{"secret default", []schema.Field{{Name: "p", Type: schema.TypeSecret, Default: "x"}}, "fields[0].default", "cannot have a default"},
		{"object default", []schema.Field{{Name: "o", Type: schema.TypeObject, Fields: []schema.Field{str}, Default: map[string]any{}}}, "fields[0].default", "cannot have a default"},
		{"default of wrong type", []schema.Field{{Name: "n", Type: schema.TypeInt, Default: "ten"}}, "fields[0].default", "whole number"},
		{"too deep", []schema.Field{nest(5)}, "fields[0].fields[0].fields[0].fields[0].fields[0].fields", "levels deep"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := problemsOf(t, schema.Schema{Fields: c.fields}.Check())
			if !hasProblem(ps, c.path, c.msg) {
				t.Fatalf("problems = %+v, want %q at %q", ps, c.msg, c.path)
			}
		})
	}
}

func TestCheckRejectsTooManyFields(t *testing.T) {
	fields := make([]schema.Field, schema.MaxFields+1)
	for i := range fields {
		fields[i] = schema.Field{Name: fmt.Sprintf("f%d", i), Type: schema.TypeString}
	}
	ps := problemsOf(t, schema.Schema{Fields: fields}.Check())
	if !hasProblem(ps, "", "at most 100 fields") {
		t.Fatalf("problems = %+v", ps)
	}
}

func TestScopeHelpers(t *testing.T) {
	s := specExample()
	names := func(fs []schema.Field) string {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Name
		}
		return strings.Join(out, ",")
	}
	if got := names(s.GroupFields()); got != "php_default,max_upload_mb" {
		t.Errorf("GroupFields = %s", got)
	}
	if got := names(s.HostFields()); got != "vhosts" {
		t.Errorf("HostFields = %s", got)
	}
	m := s.MembershipFields()
	if got := names(m); got != "vhosts,php_default" {
		t.Fatalf("MembershipFields = %s", got)
	}
	if m[1].Required || m[1].Default != nil {
		t.Errorf("an override must be optional with no schema default: %+v", m[1])
	}
	if s.Fields[1].Default != "8.3" {
		t.Errorf("MembershipFields mutated the schema: %+v", s.Fields[1])
	}
	if (schema.Field{}).EffectiveScope() != schema.ScopeGroup {
		t.Error("an unset scope must mean group")
	}
}

// TestCheckStringLengthBoundsReportAtTheirAttribute pins that an invalid
// string min or max is reported at that attribute, each on its own.
func TestCheckStringLengthBoundsReportAtTheirAttribute(t *testing.T) {
	cases := []struct {
		name     string
		min, max *float64
		want     []string // paths with a "whole numbers" problem
	}{
		{"fractional max", nil, ptr(2.5), []string{"fields[0].max"}},
		{"negative max", nil, ptr(-1.0), []string{"fields[0].max"}},
		{"fractional min", ptr(0.5), nil, []string{"fields[0].min"}},
		{"both invalid", ptr(-1.0), ptr(1.5), []string{"fields[0].min", "fields[0].max"}},
		{"both valid", ptr(1.0), ptr(8.0), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := schema.Schema{Fields: []schema.Field{{Name: "s", Type: schema.TypeString, Min: c.min, Max: c.max}}}.Check()
			var got []string
			if err != nil {
				for _, p := range problemsOf(t, err) {
					if strings.Contains(p.Message, "whole numbers") {
						got = append(got, p.Path)
					}
				}
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("whole-number problems at %v, want %v", got, c.want)
			}
		})
	}
}
