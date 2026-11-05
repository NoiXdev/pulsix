package varsource

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

func TestParseMarker(t *testing.T) {
	hundredOne := make([]string, 101)
	for i := range hundredOne {
		hundredOne[i] = fmt.Sprintf("o%d", i)
	}
	cases := []struct {
		comment    string
		isMarker   bool
		unreadable bool
		want       marker
	}{
		{"# pulsix: HTTP port", true, false, marker{Label: "HTTP port"}},
		{"#pulsix: required HTTP port", true, false, marker{Required: true, Label: "HTTP port"}},
		{"# pulsix: enum[a, b c] required Mode", true, false, marker{Enum: []string{"a", "b c"}, Required: true, Label: "Mode"}},
		{"# pulsix:", true, false, marker{}},
		{"#   pulsix:   \t", true, false, marker{}},
		{"# pulsix: secret required", true, false, marker{Secret: true, Required: true}},
		{"# pulsix:\trequired\tsecret  API  token  ", true, false, marker{Secret: true, Required: true, Label: "API  token"}},
		{"# pulsix: secrets Pw", true, false, marker{Label: "secrets Pw"}},
		{"# pulsix: Required x", true, false, marker{Label: "Required x"}},
		{"# pulsix: required HTTP secret", true, false, marker{Required: true, Label: "HTTP secret"}},
		{"# pulsix: enum[ x ,y]", true, false, marker{Enum: []string{"x", "y"}}},
		{"# Pulsix: x", false, false, marker{}},
		{"# note", false, false, marker{}},
		{"# see pulsix: x", false, false, marker{}},
		{"", false, false, marker{}},
		{"# pulsix:required", true, true, marker{}},
		{"# pulsix: enum[a", true, true, marker{}},
		{"# pulsix: enum[]", true, true, marker{}},
		{"# pulsix: enum[ ]", true, true, marker{}},
		{"# pulsix: enum[a,,b]", true, true, marker{}},
		{"# pulsix: enum[a,a]", true, true, marker{}},
		{"# pulsix: enum[a]required", true, true, marker{}},
		{"# pulsix: enum[a] enum[b]", true, true, marker{}},
		{"# pulsix: enum[" + strings.Join(hundredOne, ",") + "]", true, true, marker{}},
		{"# pulsix: required required", true, true, marker{}},
		{"# pulsix: secret secret", true, true, marker{}},
		{"# pulsix: enum[a] secret", true, true, marker{}},
		{"# pulsix: secret enum[a]", true, true, marker{}},
	}
	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			m, isMarker, err := parseMarker(tc.comment)
			if isMarker != tc.isMarker {
				t.Fatalf("isMarker = %v, want %v", isMarker, tc.isMarker)
			}
			if tc.unreadable {
				if err == nil {
					t.Fatalf("err = nil, want unreadable (marker %+v)", m)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(m, tc.want) {
				t.Fatalf("marker = %+v, want %+v", m, tc.want)
			}
		})
	}
}

// playVars parses a play whose vars hold lines; the first is on line 3.
func playVars(t *testing.T, lines ...string) *yaml.Node {
	t.Helper()
	src := "- hosts: all\n  vars:\n"
	for _, l := range lines {
		src += "    " + l + "\n"
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	play := doc.Content[0].Content[0]
	return play.Content[3]
}

func fromMarker(line int, f schema.Field) schema.Field {
	f.Origin = schema.OriginSource
	f.Source = &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: line}
	return f
}

func TestMarkerField(t *testing.T) {
	strList := func(def any) *schema.Field {
		return &schema.Field{Name: "hosts", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}, Default: def}
	}
	cases := []struct {
		name     string
		vars     []string
		key      string
		line     int
		isMarker bool
		field    *schema.Field // nil: not a field
		notes    []string
	}{
		{"int with label", []string{"http_port: 8080  # pulsix: required HTTP port"}, "http_port", 3, true,
			&schema.Field{Name: "http_port", Type: schema.TypeInt, Default: 8080, Required: true, Label: "HTTP port"}, nil},
		{"enum", []string{"mode: blue  # pulsix: enum[blue,green]"}, "mode", 3, true,
			&schema.Field{Name: "mode", Type: schema.TypeEnum, Options: []string{"blue", "green"}, Default: "blue"}, nil},
		{"enum without value", []string{"mode:  # pulsix: enum[blue,green]"}, "mode", 3, true,
			&schema.Field{Name: "mode", Type: schema.TypeEnum, Options: []string{"blue", "green"}}, nil},
		{"enum value not an option", []string{"mode: red  # pulsix: enum[blue,green]"}, "mode", 3, true, nil,
			[]string{"warning marker_enum_value site.yml:3"}},
		{"enum on a list", []string{"mode: [blue]  # pulsix: enum[blue,green]"}, "mode", 3, true, nil,
			[]string{"warning marker_value_type site.yml:3"}},
		{"secret", []string{"token: abc  # pulsix: secret API token"}, "token", 3, true,
			&schema.Field{Name: "token", Type: schema.TypeSecret, Label: "API token"}, nil},
		{"secret on a list", []string{"token: [a]  # pulsix: secret"}, "token", 3, true, nil,
			[]string{"warning marker_value_type site.yml:3"}},
		{"float", []string{"ratio: 0.5  # pulsix:"}, "ratio", 3, true,
			&schema.Field{Name: "ratio", Type: schema.TypeFloat, Default: 0.5}, nil},
		{"bool", []string{"debug: false  # pulsix:"}, "debug", 3, true,
			&schema.Field{Name: "debug", Type: schema.TypeBool, Default: false}, nil},
		{"YAML 1.1 bool", []string{"debug: yes  # pulsix:"}, "debug", 3, true,
			&schema.Field{Name: "debug", Type: schema.TypeBool, Default: true}, nil},
		{"quoted yes is a string", []string{`answer: "yes"  # pulsix:`}, "answer", 3, true,
			&schema.Field{Name: "answer", Type: schema.TypeString, Default: "yes"}, nil},
		{"list of YAML 1.1 bools", []string{"flags: [on, off]  # pulsix:"}, "flags", 3, true,
			&schema.Field{Name: "flags", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeBool}, Default: []any{true, false}}, nil},
		{"quoted number is a string", []string{`version: "1.2"  # pulsix:`}, "version", 3, true,
			&schema.Field{Name: "version", Type: schema.TypeString, Default: "1.2"}, nil},
		{"list of strings", []string{"hosts: [a, b]  # pulsix:"}, "hosts", 3, true, strList([]any{"a", "b"}), nil},
		{"empty list", []string{"hosts: []  # pulsix:"}, "hosts", 3, true, strList([]any{}), nil},
		{"block list, marker on the key", []string{"hosts:  # pulsix: Hosts", "  - a", "  - b"}, "hosts", 3, true,
			func() *schema.Field { f := strList([]any{"a", "b"}); f.Label = "Hosts"; return f }(), nil},
		{"list of ints", []string{"ports: [80, 443]  # pulsix:"}, "ports", 3, true,
			&schema.Field{Name: "ports", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}, Default: []any{80, 443}}, nil},
		{"mixed list", []string{"mixed: [1, a]  # pulsix:"}, "mixed", 3, true, nil,
			[]string{"warning marker_value_type site.yml:3"}},
		{"list with a null", []string{"mixed: [a, ~]  # pulsix:"}, "mixed", 3, true, nil,
			[]string{"warning marker_value_type site.yml:3"}},
		{"mapping", []string{"conf: {a: 1}  # pulsix:"}, "conf", 3, true, nil,
			[]string{"warning marker_value_type site.yml:3"}},
		{"custom tag", []string{"pw: !vault abc  # pulsix:"}, "pw", 3, true, nil,
			[]string{"warning marker_value_type site.yml:3"}},
		{"custom tag secret", []string{"pw: !vault abc  # pulsix: secret"}, "pw", 3, true,
			&schema.Field{Name: "pw", Type: schema.TypeSecret}, nil},
		{"empty value", []string{"empty:  # pulsix: Label"}, "empty", 3, true,
			&schema.Field{Name: "empty", Type: schema.TypeString, Label: "Label"}, nil},
		{"templated value", []string{`greeting: "{{ x }}"  # pulsix:`}, "greeting", 3, true,
			&schema.Field{Name: "greeting", Type: schema.TypeString}, []string{"hint templated_default site.yml:3"}},
		{"alias resolves to its anchor", []string{"base: &ports [80, 443]", "ports: *ports  # pulsix: Ports"}, "ports", 4, true,
			&schema.Field{Name: "ports", Label: "Ports", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeInt}, Default: []any{80, 443}}, nil},
		{"invalid name", []string{"HTTP_PORT: 80  # pulsix:"}, "HTTP_PORT", 3, true, nil,
			[]string{"warning invalid_name site.yml:3"}},
		{"unreadable marker", []string{"mode: a  # pulsix: enum[a"}, "mode", 3, true, nil,
			[]string{"warning marker_unreadable site.yml:3"}},
		{"no marker", []string{"plain: 1  # just a note"}, "plain", 3, false, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := playVars(t, tc.vars...)
			var key, value *yaml.Node
			for i := 0; i+1 < len(vars.Content); i += 2 {
				if vars.Content[i].Value == tc.key {
					key, value = vars.Content[i], vars.Content[i+1]
				}
			}
			if key == nil {
				t.Fatalf("no key %s", tc.key)
			}
			f, notes, isMarker, ok := markedVar(newYAMLFile("site.yml"), key, value)
			if isMarker != tc.isMarker || ok != (tc.field != nil) {
				t.Fatalf("isMarker, ok = %v, %v; want %v, %v (notes %v)", isMarker, ok, tc.isMarker, tc.field != nil, notes)
			}
			if ok {
				wantFields(t, []schema.Field{f}, []schema.Field{fromMarker(tc.line, *tc.field)})
			}
			wantNotes(t, notes, tc.notes...)
		})
	}
}
