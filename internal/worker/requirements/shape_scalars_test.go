package requirements

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// A source field must be a string to both YAML loaders (backlog 7.105):
// yaml.v3 reads 1e3, 1e+3 and 0o17 as numbers where galaxy's PyYAML reads
// them as strings, so a number there would be a path galaxy follows that
// checkSources never resolved. The quoted spelling is a string to both and
// goes through checkSources.
func TestPlanRefusesNonStringSourceScalars(t *testing.T) {
	fields := []struct {
		name, field string
		file        func(v string) string
	}{
		{"role src", "role src", func(v string) string { return "- {src: " + v + ", name: r}\n" }},
		{"role name used as the source", "role name", func(v string) string { return "- {name: " + v + "}\n" }},
		{"role key used as the source", "role name", func(v string) string { return "- {role: " + v + "}\n" }},
		{"collection name", "collection name", func(v string) string { return "collections:\n  - {name: " + v + "}\n" }},
		{"collection source", "collection source", func(v string) string {
			return "collections:\n  - {name: ns.c, type: dir, source: " + v + "}\n"
		}},
	}
	for _, f := range fields {
		for _, v := range []string{"1e3", "1e+3", ".5e3", "0o17", "true", "1.5"} {
			t.Run(f.name+" "+v, func(t *testing.T) {
				want := "requirements.yml is not a requirements file: the " + f.field + " " + v +
					" is not a string; quote it"
				got, err := parseFile("requirements.yml", []byte(f.file(v)))
				if err == nil || err.Error() != want {
					t.Fatalf("parseFile = %+v, %v; want %q", got, err, want)
				}
			})

			if strings.Contains(v, ".") {
				continue // quoted, a dotted name is a name the other checks refuse for their own reasons
			}
			t.Run(f.name+" quoted "+v, func(t *testing.T) {
				got, err := parseFile("requirements.yml", []byte(f.file(`"`+v+`"`)))
				if err != nil {
					t.Fatalf("parseFile = %v, want the quoted form accepted", err)
				}
				if !slices.Contains(got.sources, v) {
					t.Fatalf("sources = %q, want %q among them", got.sources, v)
				}
				clone := refusingFS{fstest.MapFS{
					v + "/galaxy.yml": {Data: []byte("namespace: ns\n")},
					v + "/out":        link("../../outside"),
				}}
				want := `requirements.yml: the source "` + v + `" holds the link "` + v + `/out", which leads outside the repository`
				if err := checkSources(t.Context(), clone, testCloneDir, "", got); err == nil || err.Error() != want {
					t.Fatalf("checkSources = %v, want %q", err, want)
				}
			})
		}
	}
}

// Null stays a missing field and strings stay strings; only a number or a
// boolean is refused.
func TestPlanAcceptsNullAndStringSourceScalars(t *testing.T) {
	for _, data := range []string{
		"- {src: , name: r}\n",
		"- {src: ~, name: r}\n",
		"- {src: ./r.tar.gz, name: r}\n",
		"collections:\n  - {name: ns.c, type: dir, source: ~}\n",
		"collections:\n  - {name: ~, type: file, source: ./c.tar.gz}\n",
	} {
		if _, err := parseFile("requirements.yml", []byte(data)); err != nil && strings.Contains(err.Error(), "is not a string") {
			t.Errorf("parseFile(%q) = %v, want no refusal for a string or null", data, err)
		}
	}
}
