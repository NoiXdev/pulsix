package varsource

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// specWithOptions is a role argument spec with n str options o0…o(n-1).
func specWithOptions(n int) string {
	var b strings.Builder
	b.WriteString("argument_specs:\n  main:\n    options:\n")
	for i := range n {
		fmt.Fprintf(&b, "      o%d: {type: str}\n", i)
	}
	return b.String()
}

// TestFieldsCut: whenever a cap leaves declared top-level fields out, the
// playbook says so, so a missing name is not taken as removed.
func TestFieldsCut(t *testing.T) {
	var prompts, markers strings.Builder
	for i := range 101 {
		fmt.Fprintf(&prompts, "    - {name: p%d, default: x, private: false}\n", i)
	}
	for i := range 101 {
		fmt.Fprintf(&markers, "    m%d: 1  # pulsix:\n", i)
	}
	cases := []struct {
		name  string
		files map[string]string
		cut   bool
	}{
		{"nothing cut", map[string]string{"site.yml": "- hosts: all\n  roles: [r]\n", "roles/r/meta/argument_specs.yml": specWithOptions(100)}, false},
		{"one entry point over 100", map[string]string{"site.yml": "- hosts: all\n  roles: [r]\n", "roles/r/meta/argument_specs.yml": specWithOptions(101)}, true},
		{"the spec file's option budget", map[string]string{"site.yml": "- hosts: all\n  roles: [r]\n",
			"roles/r/meta/argument_specs.yml": "argument_specs:\n  main:\n    options:\n      a: {type: dict, options: " + wideMapping(maxSpecFields, "x: {type: str}") + "}\n      b: {type: str}\n"}, true},
		{"the sidecar over 100", map[string]string{"site.yml": "- hosts: all\n", "site.argument_specs.yml": specWithOptions(101)}, true},
		{"more than 100 prompts", map[string]string{"site.yml": "- hosts: all\n  vars_prompt:\n" + prompts.String()}, true},
		{"more than 100 in all", map[string]string{"site.yml": "- hosts: all\n  vars:\n" + markers.String()}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, data := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(data)}
			}
			pb := ParsePlaybook(fsys, "site.yml")
			if pb.Failed || pb.FieldsCut != tc.cut {
				t.Fatalf("failed = %v, fields cut = %v, want %v (%d fields)", pb.Failed, pb.FieldsCut, tc.cut, len(pb.Fields))
			}
		})
	}
}

// TestUnreadableNames: a variable a read source declares but that maps
// onto no field is listed as unreadable, not dropped silently.
func TestUnreadableNames(t *testing.T) {
	fsys := fstest.MapFS{
		"site.yml": {Data: []byte("- hosts: all\n  vars:\n    x: 1  # pulsix: enum[a,b Label\n    m: {a: 1}  # pulsix:\n    ok: 1  # pulsix:\n  roles: [r]\n")},
		"roles/r/meta/argument_specs.yml": {Data: []byte("argument_specs:\n  main:\n    options:\n" +
			"      raw_opt: {type: raw}\n      ansible_port: {type: str}\n      Port: {type: str}\n      ok: {type: raw}\n      good: {type: str}\n")},
	}
	pb := ParsePlaybook(fsys, "site.yml")
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"ok", "good"}) {
		t.Fatalf("fields = %q", got)
	}
	if want := []string{"ansible_port", "m", "raw_opt", "x"}; !reflect.DeepEqual(pb.Unreadable, want) {
		t.Fatalf("unreadable = %q, want %q", pb.Unreadable, want)
	}
	if pb.FieldsCut {
		t.Fatal("fields cut")
	}
}

// TestBrokenEntryPoint: an entry point (or its options) that is no mapping
// leaves the role or sidecar unreadable, not read without fields.
func TestBrokenEntryPoint(t *testing.T) {
	broken := "argument_specs:\n  main:\n    options: [a]\n"
	fsys := fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  roles: [r]\n")},
		"site.argument_specs.yml":         {Data: []byte(broken)},
		"roles/r/meta/argument_specs.yml": {Data: []byte("argument_specs:\n  main: 5\n")},
	}
	pb := ParsePlaybook(fsys, "site.yml")
	wantRoles(t, pb.Roles, RoleUse{"r", "main", RoleUnreadable})
	wantFiles(t, pb.Files, FileUse{Path: "site.yml", State: FileRead}, FileUse{Path: "site.argument_specs.yml", State: FileUnreadable})
}

// TestVarsAsAList: play vars written as a list of mappings are read like
// a mapping: plain names are set, a marked one is a field.
func TestVarsAsAList(t *testing.T) {
	src := "- hosts: all\n  vars:\n    - plain: 1\n    - marked: 2  # pulsix:\n"
	pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(src)}}, "site.yml")
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"marked"}) {
		t.Fatalf("fields = %q", got)
	}
	if !reflect.DeepEqual(pb.SetInPlaybook, []string{"plain"}) {
		t.Fatalf("set in playbook = %q", pb.SetInPlaybook)
	}
	wantNotes(t, pb.Notes)
}
