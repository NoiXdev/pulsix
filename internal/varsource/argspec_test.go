package varsource

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func TestArgumentSpecTypeMapping(t *testing.T) {
	str := schema.Field{Name: "x", Type: schema.TypeString}
	unsupported := []string{"warning unsupported_type " + specFile + ":4"}
	cases := []struct {
		name  string
		spec  []byte
		field *schema.Field // nil: skipped
		notes []string
	}{
		{"str", specYAML("x: {type: str}"), &str, nil},
		{"path", specYAML("x: {type: path}"), &str, nil},
		{"type absent", specYAML("x: {required: false}"), &str, nil},
		{"int", specYAML("x: {type: int}"), &schema.Field{Name: "x", Type: schema.TypeInt}, nil},
		{"float", specYAML("x: {type: float}"), &schema.Field{Name: "x", Type: schema.TypeFloat}, nil},
		{"bool", specYAML("x: {type: bool}"), &schema.Field{Name: "x", Type: schema.TypeBool}, nil},
		{"list of str", specYAML("x: {type: list, elements: str}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}}, nil},
		{"list of int", specYAML("x: {type: list, elements: int}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}}, nil},
		{"list of dict with options", specYAML(
			"x:",
			"  type: list",
			"  elements: dict",
			"  options:",
			"    name: {type: str, required: true}",
			"    port: {type: int}",
		), &schema.Field{Name: "x", Type: schema.TypeRepeater, Fields: []schema.Field{
			{Name: "name", Type: schema.TypeString, Required: true},
			{Name: "port", Type: schema.TypeInt},
		}}, nil},
		{"dict with options", specYAML(
			"x:",
			"  type: dict",
			"  options:",
			"    user: {type: str}",
		), &schema.Field{Name: "x", Type: schema.TypeObject, Fields: []schema.Field{
			{Name: "user", Type: schema.TypeString},
		}}, nil},
		{"choices", specYAML("x: {choices: [a, b]}"),
			&schema.Field{Name: "x", Type: schema.TypeEnum, Options: []string{"a", "b"}}, nil},
		{"list with choices", specYAML("x: {type: list, elements: str, choices: [a, b]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeEnum, Options: []string{"a", "b"}}}, nil},
		{"raw", specYAML("x: {type: raw}"), nil, unsupported},
		{"dict without options", specYAML("x: {type: dict}"), nil, unsupported},
		{"dict with empty options", specYAML("x: {type: dict, options: {}}"), nil, unsupported},
		{"list without elements", specYAML("x: {type: list}"), nil, unsupported},
		{"list of dict without options", specYAML("x: {type: list, elements: dict}"), nil, unsupported},
		{"list of raw", specYAML("x: {type: list, elements: raw}"), nil, unsupported},
		{"bits", specYAML("x: {type: bits}"), nil, unsupported},
		{"bytes", specYAML("x: {type: bytes}"), nil, unsupported},
		{"jsonarg", specYAML("x: {type: jsonarg}"), nil, unsupported},
		{"json", specYAML("x: {type: json}"), nil, unsupported},
		{"sid", specYAML("x: {type: sid}"), nil, unsupported},
		{"unknown", specYAML("x: {type: frobnicate}"), nil, unsupported},
		{"unsupported sub-field", specYAML(
			"x:",
			"  type: dict",
			"  options:",
			"    keep: {type: str}",
			"    blob: {type: raw}",
		), &schema.Field{Name: "x", Type: schema.TypeObject, Fields: []schema.Field{
			{Name: "keep", Type: schema.TypeString},
		}}, []string{"warning unsupported_type " + specFile + ":8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields, notes := readSpec(t, tc.spec)
			var want []schema.Field
			if tc.field != nil {
				want = []schema.Field{fromSpec(4, *tc.field)}
			}
			wantFields(t, fields, want)
			wantNotes(t, notes, tc.notes...)
		})
	}
}

// longDigits is a 70-digit number: too long to decode as one.
var longDigits = "1" + strings.Repeat("2", 69)

func TestArgumentSpecAttributes(t *testing.T) {
	cases := []struct {
		name  string
		spec  []byte
		field *schema.Field // nil: skipped
		notes []string
	}{
		{"required", specYAML("x: {type: str, required: true}"),
			&schema.Field{Name: "x", Type: schema.TypeString, Required: true}, nil},
		{"int default", specYAML("x: {type: int, default: 80}"),
			&schema.Field{Name: "x", Type: schema.TypeInt, Default: 80}, nil},
		{"list default", specYAML("x: {type: list, elements: str, default: [a, b]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}, Default: []any{"a", "b"}}, nil},
		{"null default", specYAML("x: {type: str, default: null}"),
			&schema.Field{Name: "x", Type: schema.TypeString}, nil},
		{"description string", specYAML(`x: {type: int, description: "Port to listen on\nUsed by nginx."}`),
			&schema.Field{Name: "x", Type: schema.TypeInt, Label: "Port to listen on", Description: "Used by nginx."}, nil},
		{"description list", specYAML("x: {type: int, description: [A, B, C]}"),
			&schema.Field{Name: "x", Type: schema.TypeInt, Label: "A", Description: "B\nC"}, nil},
		{"non-string choices", specYAML("x: {type: int, choices: [1, 2], default: 2}"),
			&schema.Field{Name: "x", Type: schema.TypeEnum, Options: []string{"1", "2"}, Default: "2"},
			[]string{"warning choices_converted " + specFile + ":4"}},
		{"list with non-string choices", specYAML("x: {type: list, elements: int, choices: [1, 2], default: [2]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeEnum, Options: []string{"1", "2"}}, Default: []any{"2"}},
			[]string{"warning choices_converted " + specFile + ":4"}},
		{"choices on a dict are ignored", specYAML("x: {type: dict, choices: [a], options: {a: {}}}"),
			&schema.Field{Name: "x", Type: schema.TypeObject, Fields: []schema.Field{{Name: "a", Type: schema.TypeString}}}, nil},
		{"choices not a list", specYAML("x: {choices: a}"), nil,
			[]string{"warning field_invalid " + specFile + ":4"}},
		{"choices not single values", specYAML("x: {choices: [[a]]}"), nil,
			[]string{"warning field_invalid " + specFile + ":4"}},
		{"no_log drops the default", specYAML("x: {type: str, no_log: true, default: x}"),
			&schema.Field{Name: "x", Type: schema.TypeSecret},
			[]string{"warning secret_default_dropped " + specFile + ":4"}},
		{"no_log required", specYAML("x: {no_log: true, required: true}"),
			&schema.Field{Name: "x", Type: schema.TypeSecret, Required: true}, nil},
		{"no_log on a list", specYAML("x: {type: list, elements: str, no_log: true}"), nil,
			[]string{"warning no_log_collection " + specFile + ":4"}},
		{"no_log on a dict", specYAML("x: {type: dict, options: {a: {}}, no_log: true}"), nil,
			[]string{"warning no_log_collection " + specFile + ":4"}},
		{"default not among choices", specYAML("x: {choices: [a, b], default: c}"),
			&schema.Field{Name: "x", Type: schema.TypeEnum, Options: []string{"a", "b"}},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"default of the wrong type", specYAML("x: {type: int, default: eighty}"),
			&schema.Field{Name: "x", Type: schema.TypeInt},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"not a finite default", specYAML("x: {type: float, default: .nan}"),
			&schema.Field{Name: "x", Type: schema.TypeFloat},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"default on a dict", specYAML(
			"x:",
			"  type: dict",
			"  default: {user: root}",
			"  options:",
			"    user: {type: str}",
		), &schema.Field{Name: "x", Type: schema.TypeObject, Fields: []schema.Field{{Name: "user", Type: schema.TypeString}}},
			[]string{"warning default_dropped " + specFile + ":4"}},
		{"default on a list of dicts", specYAML(
			"x:",
			"  type: list",
			"  elements: dict",
			"  default: []",
			"  options:",
			"    user: {type: str}",
		), &schema.Field{Name: "x", Type: schema.TypeRepeater, Fields: []schema.Field{{Name: "user", Type: schema.TypeString}}},
			[]string{"warning default_dropped " + specFile + ":4"}},
		{"default a mapping", specYAML("x: {type: str, default: {a: 1}}"),
			&schema.Field{Name: "x", Type: schema.TypeString},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"integer beyond int64", specYAML("x: {type: int, default: 18446744073709551615}"),
			&schema.Field{Name: "x", Type: schema.TypeInt},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"YAML 1.1 bool default", specYAML("x: {type: bool, default: yes}"),
			&schema.Field{Name: "x", Type: schema.TypeBool, Default: true}, nil},
		{"YAML 1.1 bool attributes", specYAML("x: {type: bool, required: yes}"),
			&schema.Field{Name: "x", Type: schema.TypeBool, Required: true}, nil},
		{"YAML 1.1 no_log", specYAML("x: {no_log: on}"),
			&schema.Field{Name: "x", Type: schema.TypeSecret}, nil},
		{"choices on a bool are ignored", specYAML("x: {type: bool, choices: [yes, no], default: no}"),
			&schema.Field{Name: "x", Type: schema.TypeBool, Default: false}, nil},
		{"bool choices on a string are converted", specYAML("x: {choices: [yes, no]}"),
			&schema.Field{Name: "x", Type: schema.TypeEnum, Options: []string{"yes", "no"}},
			[]string{"warning choices_converted " + specFile + ":4"}},
		{"empty choices are ignored", specYAML("x: {choices: []}"),
			&schema.Field{Name: "x", Type: schema.TypeString}, nil},
		{"string default from a number", specYAML("x: {type: str, default: 8080}"),
			&schema.Field{Name: "x", Type: schema.TypeString, Default: "8080"}, nil},
		{"string default from a bool", specYAML("x: {type: path, default: yes}"),
			&schema.Field{Name: "x", Type: schema.TypeString, Default: "yes"}, nil},
		{"string list default from numbers", specYAML("x: {type: list, elements: str, default: [1, b]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}, Default: []any{"1", "b"}}, nil},
		{"int list default", specYAML("x: {type: list, elements: int, default: [80, 443]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}, Default: []any{80, 443}}, nil},
		{"nested list default", specYAML("x: {type: list, elements: int, default: [[80]]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"long number default of a str", specYAML("x: {type: str, default: " + longDigits + "}"),
			&schema.Field{Name: "x", Type: schema.TypeString, Default: longDigits}, nil},
		{"long number default of a str list", specYAML("x: {type: list, elements: path, default: [" + longDigits + "]}"),
			&schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}, Default: []any{longDigits}}, nil},
		{"long number default of an enum", specYAML("x: {choices: [" + longDigits + ", b], default: " + longDigits + "}"),
			&schema.Field{Name: "x", Type: schema.TypeEnum, Options: []string{longDigits, "b"}, Default: longDigits},
			[]string{"warning choices_converted " + specFile + ":4"}},
		{"long number default of an int", specYAML("x: {type: int, default: " + longDigits + "}"),
			&schema.Field{Name: "x", Type: schema.TypeInt},
			[]string{"warning default_invalid " + specFile + ":4"}},
		{"spec not a mapping", specYAML("x: 80"), nil,
			[]string{"warning field_invalid " + specFile + ":4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields, notes := readSpec(t, tc.spec)
			var want []schema.Field
			if tc.field != nil {
				want = []schema.Field{fromSpec(4, *tc.field)}
			}
			wantFields(t, fields, want)
			wantNotes(t, notes, tc.notes...)
		})
	}
}

// TestArgumentSpecMergeKeys: a spec that shares attributes through YAML
// merge keys reads like one that spells them out; explicit keys win.
func TestArgumentSpecMergeKeys(t *testing.T) {
	data := []byte(`common: &common
  type: int
  default: 80
  required: false
labelled: &labelled
  description: Label
  default: 90
argument_specs:
  main:
    options:
      x:
        <<: *common
        required: true
      y:
        <<: [*labelled, *common]
`)
	fields, notes := readSpec(t, data)
	wantFields(t, fields, []schema.Field{
		fromSpec(11, schema.Field{Name: "x", Type: schema.TypeInt, Default: 80, Required: true}),
		fromSpec(14, schema.Field{Name: "y", Type: schema.TypeInt, Default: 90, Label: "Label"}),
	})
	wantNotes(t, notes)
}

// TestArgumentSpecAliasBudget: aliases can make a small file describe an
// exponentially large tree of options. Mapping stops at the file's budget
// and the field being mapped when it ran out is skipped whole.
func TestArgumentSpecAliasBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 {type: str}\n")
	for level := 1; level <= 4; level++ {
		fmt.Fprintf(&b, "a%d: &a%d {type: dict, options: {", level, level)
		for i := range 10 {
			fmt.Fprintf(&b, "o%d: *a%d, ", i, level-1)
		}
		b.WriteString("}}\n")
	}
	b.WriteString("argument_specs:\n  main:\n    options:\n      plain: {type: str}\n")
	for i := range 10 {
		fmt.Fprintf(&b, "      bomb%d: *a4\n", i)
	}
	fields, notes := readSpec(t, []byte(b.String()))
	wantFields(t, fields, []schema.Field{fromSpec(9, schema.Field{Name: "plain", Type: schema.TypeString})})
	wantNotes(t, notes, "warning too_many_fields "+specFile+":10")
}

func TestArgumentSpecEntryPoints(t *testing.T) {
	data := []byte(`argument_specs:
  main:
    short_description: Main
    options:
      main_opt: {type: str}
  install:
    options:
      install_opt: {type: int}
`)
	t.Run("main by default", func(t *testing.T) {
		fields, notes, readOK, entryFound := argumentSpecFields(data, specFile, "nginx", "", schema.SourceArgumentSpec)
		if !readOK || !entryFound {
			t.Fatalf("readOK = %v, entryFound = %v", readOK, entryFound)
		}
		wantFields(t, fields, []schema.Field{fromSpec(5, schema.Field{Name: "main_opt", Type: schema.TypeString})})
		wantNotes(t, notes)
	})
	t.Run("named entry point", func(t *testing.T) {
		fields, notes, _, entryFound := argumentSpecFields(data, specFile, "nginx", "install", schema.SourceArgumentSpec)
		if !entryFound {
			t.Fatal("entryFound = false")
		}
		want := fromSpec(8, schema.Field{Name: "install_opt", Type: schema.TypeInt})
		want.Source.EntryPoint = "install"
		wantFields(t, fields, []schema.Field{want})
		wantNotes(t, notes)
	})
	t.Run("missing entry point", func(t *testing.T) {
		fields, notes, readOK, entryFound := argumentSpecFields(data, specFile, "nginx", "upgrade", schema.SourceArgumentSpec)
		if !readOK || entryFound {
			t.Fatalf("readOK = %v, entryFound = %v, want true, false", readOK, entryFound)
		}
		wantFields(t, fields, nil)
		wantNotes(t, notes, "hint entry_point_missing "+specFile+":1")
	})
	t.Run("entry point not a mapping", func(t *testing.T) {
		fields, notes, readOK, entryFound := argumentSpecFields([]byte("argument_specs:\n  main: 5\n"), specFile, "nginx", "", schema.SourceArgumentSpec)
		if !readOK || !entryFound {
			t.Fatalf("readOK = %v, entryFound = %v, want true, true", readOK, entryFound)
		}
		wantFields(t, fields, nil)
		wantNotes(t, notes, "warning field_invalid "+specFile+":2")
	})
	t.Run("no argument_specs key", func(t *testing.T) {
		_, notes, readOK, entryFound := argumentSpecFields([]byte("{}\n"), specFile, "nginx", "", schema.SourceArgumentSpec)
		if !readOK || entryFound {
			t.Fatalf("readOK = %v, entryFound = %v, want true, false", readOK, entryFound)
		}
		wantNotes(t, notes, "hint entry_point_missing "+specFile+":0")
	})
	t.Run("empty file", func(t *testing.T) {
		_, notes, readOK, entryFound := argumentSpecFields(nil, specFile, "nginx", "", schema.SourceArgumentSpec)
		if !readOK || entryFound {
			t.Fatalf("readOK = %v, entryFound = %v, want true, false", readOK, entryFound)
		}
		wantNotes(t, notes, "hint entry_point_missing "+specFile+":0")
	})
	t.Run("options not a mapping", func(t *testing.T) {
		fields, notes, _, entryFound := argumentSpecFields([]byte("argument_specs:\n  main:\n    options: [a]\n"), specFile, "nginx", "", schema.SourceArgumentSpec)
		if !entryFound {
			t.Fatal("entryFound = false")
		}
		wantFields(t, fields, nil)
		wantNotes(t, notes, "warning field_invalid "+specFile+":3")
	})
	t.Run("invalid yaml", func(t *testing.T) {
		fields, notes, readOK, entryFound := argumentSpecFields([]byte("argument_specs:\n  main: x: y\n"), specFile, "nginx", "", schema.SourceArgumentSpec)
		if readOK || entryFound {
			t.Fatalf("readOK = %v, entryFound = %v, want false, false", readOK, entryFound)
		}
		wantFields(t, fields, nil)
		wantNotes(t, notes, "warning invalid_yaml "+specFile+":2")
	})
	t.Run("sidecar", func(t *testing.T) {
		fields, _, _, _ := argumentSpecFields(data, "deploy.argument_specs.yml", "", "", schema.SourceSidecar)
		want := schema.Field{Name: "main_opt", Type: schema.TypeString, Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceSidecar, EntryPoint: "main", File: "deploy.argument_specs.yml", Line: 5}}
		wantFields(t, fields, []schema.Field{want})
	})
}
