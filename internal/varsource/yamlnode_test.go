package varsource

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// wideMapping is a flow mapping with n keys k0…k(n-1) plus extra entries.
func wideMapping(n int, extra string) string {
	var b strings.Builder
	b.WriteString("{" + extra)
	for i := range n {
		fmt.Fprintf(&b, ", k%d: 0", i)
	}
	b.WriteString("}")
	return b.String()
}

// TestParseWorkIsBoundedWideAlias: one 40 000-key mapping aliased as the
// spec of many nested options is read once, not once per use.
func TestParseWorkIsBoundedWideAlias(t *testing.T) {
	var b strings.Builder
	b.WriteString("wide: &wide " + wideMapping(40_000, "type: str") + "\n")
	b.WriteString("nest: &nest {type: dict, options: {")
	for i := range 50 {
		fmt.Fprintf(&b, "a%d: *wide, ", i)
	}
	b.WriteString("}}\nargument_specs:\n  main:\n    options:\n")
	for i := range 100 {
		fmt.Fprintf(&b, "      o%d: *nest\n", i)
	}
	y := newYAMLFile(specFile)
	fields, notes, readOK, _ := readArgumentSpec(y, []byte(b.String()), "nginx", "", schema.SourceArgumentSpec)
	if !readOK || len(fields) != 100 || len(fields[0].Fields) != 50 {
		t.Fatalf("readOK = %v, %d fields, notes %v; want the 100 nested fields read", readOK, len(fields), notes)
	}
	wantNotes(t, notes)
	if v := y.visits(); v > 41_000 {
		t.Fatalf("visited %d nodes, want each mapping read once (≤ 41 000)", v)
	}
}

// TestParseWorkIsBoundedMerge: many mappings each merging a wide mapping
// exhaust the file's budget; the file is given up as not read, with one
// file_too_complex warning, and the work stops at the budget.
func TestParseWorkIsBoundedMerge(t *testing.T) {
	var b strings.Builder
	b.WriteString("big: &big " + wideMapping(20_000, "type: str") + "\n")
	b.WriteString("argument_specs:\n  main:\n    options:\n")
	for i := range 2_000 {
		fmt.Fprintf(&b, "      o%d: {<<: [*big, *big, *big, *big], required: true}\n", i)
	}
	y := newYAMLFile(specFile)
	fields, notes, readOK, _ := readArgumentSpec(y, []byte(b.String()), "nginx", "", schema.SourceArgumentSpec)
	if readOK || fields != nil {
		t.Fatalf("readOK = %v, %d fields; want the file given up", readOK, len(fields))
	}
	wantNotes(t, notes, "warning file_too_complex "+specFile+":0")
	if !y.exceeded() || y.visits() > maxVisits {
		t.Fatalf("exceeded = %v, visits = %d; want the budget of %d spent and not passed", y.exceeded(), y.visits(), maxVisits)
	}
}

// TestPromptWorkIsBounded: vars_prompt entries are looked at up to the
// field limit, kept or not, and entries merging a wide mapping exhaust the
// file's budget, which the file's owner then reports.
func TestPromptWorkIsBounded(t *testing.T) {
	t.Run("entries looked at", func(t *testing.T) {
		src := ""
		for range 5_000 {
			src += "- {prompt: no name}\n"
		}
		fields, notes := promptFields(newYAMLFile("site.yml"), promptNode(t, src))
		if len(fields) != 0 || len(notes) != schema.MaxFields+1 {
			t.Fatalf("%d fields, %d notes; want none and %d invalid_name + one too_many_fields", len(fields), len(notes), schema.MaxFields)
		}
		if last := notes[len(notes)-1]; noteKey(last) != "warning too_many_fields site.yml:101" {
			t.Fatalf("last note = %s", noteKey(last))
		}
	})
	t.Run("merged entries", func(t *testing.T) {
		src := "- &big " + wideMapping(20_000, "name: x") + "\n"
		for i := range 99 {
			src += fmt.Sprintf("- {<<: *big, name: p%d, k0: 1}\n", i)
		}
		y := newYAMLFile("site.yml")
		promptFields(y, promptNode(t, src))
		if !y.exceeded() || y.visits() > maxVisits {
			t.Fatalf("exceeded = %v, visits = %d", y.exceeded(), y.visits())
		}
		if n, ok := y.tooComplex(); !ok || noteKey(n) != "warning file_too_complex site.yml:0" {
			t.Fatalf("tooComplex = %v, %v", n, ok)
		}
	})
}

// TestDuplicateKeysKeepTheLast: a key set twice in one mapping keeps its
// last value everywhere, as Ansible's loader does, with a duplicate_key
// warning at the later line.
func TestDuplicateKeysKeepTheLast(t *testing.T) {
	data := specYAML(
		"x: {type: int}",
		"y: {type: str, type: bool}",
		"x: {type: str, description: Later}",
	)
	fields, notes := readSpec(t, data)
	wantFields(t, fields, []schema.Field{
		fromSpec(6, schema.Field{Name: "x", Type: schema.TypeString, Label: "Later"}),
		fromSpec(5, schema.Field{Name: "y", Type: schema.TypeBool}),
	})
	wantNotes(t, notes,
		"warning duplicate_key "+specFile+":6",
		"warning duplicate_key "+specFile+":5",
	)
	if err := (schema.Schema{Fields: fields}).Check(); err != nil {
		t.Fatalf("Check over the returned fields: %v", err)
	}
}

// TestYAML11Booleans: plain yes/no/on/off (any of PyYAML's spellings) are
// booleans, as Ansible reads them; quoted or !!str-tagged they stay text.
func TestYAML11Booleans(t *testing.T) {
	cases := []struct {
		src  string
		typ  schema.Type
		want any
	}{
		{"yes", schema.TypeBool, true}, {"Yes", schema.TypeBool, true}, {"YES", schema.TypeBool, true},
		{"no", schema.TypeBool, false}, {"NO", schema.TypeBool, false},
		{"on", schema.TypeBool, true}, {"On", schema.TypeBool, true}, {"off", schema.TypeBool, false}, {"OFF", schema.TypeBool, false},
		{"true", schema.TypeBool, true}, {"False", schema.TypeBool, false},
		{"'yes'", schema.TypeString, "yes"}, {`"on"`, schema.TypeString, "on"}, {"!!str no", schema.TypeString, "no"},
		{"yEs", schema.TypeString, "yEs"}, {"y", schema.TypeString, "y"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte("v: "+tc.src+"\n"), &doc); err != nil {
				t.Fatal(err)
			}
			typ, v, ok := scalarOf(doc.Content[0].Content[1])
			if !ok || typ != tc.typ || v != tc.want {
				t.Fatalf("scalarOf = %v, %#v, %v; want %v, %#v", typ, v, ok, tc.typ, tc.want)
			}
		})
	}
}
