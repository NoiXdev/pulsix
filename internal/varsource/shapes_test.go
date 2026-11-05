package varsource

import (
	"strconv"
	"testing"
	"testing/fstest"
)

// A declaration site of the wrong shape never counts as read and empty.

// TestWrongShapedRoleLists: a roles: or task list that is no list hides
// the roles it may use: one warning, one unreadable role use.
func TestWrongShapedRoleLists(t *testing.T) {
	cases := []struct{ name, play string }{
		{"roles scalar", "  roles: r\n"},
		{"roles mapping", "  roles: {r: {}}\n"},
		{"tasks mapping", "  tasks: {include_role: {name: r}}\n"},
		{"pre_tasks mapping", "  pre_tasks: {include_role: {name: r}}\n"},
		{"post_tasks scalar", "  post_tasks: r\n"},
		{"handlers mapping", "  handlers: {include_role: {name: r}}\n"},
		{"block mapping", "  tasks:\n    - block: {include_role: {name: r}}\n"},
		{"rescue scalar", "  tasks:\n    - block: []\n      rescue: r\n"},
		{"always mapping", "  tasks:\n    - block: []\n      always: {import_role: {name: r}}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"site.yml":                        {Data: []byte("- hosts: all\n" + tc.play)},
				"roles/r/meta/argument_specs.yml": {Data: []byte(oneOption("r_var"))},
			}
			pb := ParsePlaybook(fsys, "site.yml")
			if pb.Failed || len(pb.Fields) != 0 {
				t.Fatalf("failed = %v, fields %q", pb.Failed, fieldNames(pb.Fields))
			}
			wantRoles(t, pb.Roles, RoleUse{"", "main", RoleUnreadable})
			if len(pb.Notes) != 1 || pb.Notes[0].Code != codeRoleInvalid {
				t.Fatalf("notes = %v, want one role_invalid", pb.Notes)
			}
		})
	}
}

// TestWrongShapedPrompts: a vars_prompt that is no list, an entry that is
// no mapping, or a name that is no text drop a declared prompt: the fields
// count as cut.
func TestWrongShapedPrompts(t *testing.T) {
	cases := []struct{ name, prompts, note string }{
		{"vars_prompt mapping", "  vars_prompt: {name: p_var, private: false}\n", "warning field_invalid site.yml:2"},
		{"entry scalar", "  vars_prompt:\n    - p_var\n", "warning field_invalid site.yml:3"},
		{"name a list", "  vars_prompt:\n    - {name: [p_var]}\n", "warning invalid_name site.yml:3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte("- hosts: all\n" + tc.prompts)}}, "site.yml")
			if pb.Failed || len(pb.Fields) != 0 || !pb.FieldsCut {
				t.Fatalf("failed = %v, %d fields, fields cut = %v; want cut", pb.Failed, len(pb.Fields), pb.FieldsCut)
			}
			wantNotes(t, pb.Notes, tc.note)
		})
	}
}

// TestWrongShapedSpecFiles: a spec file whose root, or whose
// argument_specs, is no mapping leaves the role or sidecar unreadable; an
// empty file, a null entry point or null options still declare nothing.
func TestWrongShapedSpecFiles(t *testing.T) {
	cases := []struct {
		name, spec string
		read       bool
		line       int // of the field_invalid warning, when not read
	}{
		{"root a list", "- argument_specs: {}\n", false, 1},
		{"argument_specs a list", "argument_specs:\n  - main\n", false, 1},
		{"argument_specs a scalar", "argument_specs: x\n", false, 1},
		{"empty file", "", true, 0},
		{"main null", "argument_specs:\n  main:\n", true, 0},
		{"options null", "argument_specs:\n  main:\n    options:\n", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"site.yml":                        {Data: []byte("- hosts: all\n  roles: [r]\n")},
				"site.argument_specs.yml":         {Data: []byte(tc.spec)},
				"roles/r/meta/argument_specs.yml": {Data: []byte(tc.spec)},
			}
			pb := ParsePlaybook(fsys, "site.yml")
			role, sidecar := RoleRead, FileRead
			if !tc.read {
				role, sidecar = RoleUnreadable, FileUnreadable
			}
			wantRoles(t, pb.Roles, RoleUse{"r", "main", role})
			wantFiles(t, pb.Files, FileUse{Path: "site.yml", State: FileRead}, FileUse{Path: "site.argument_specs.yml", State: sidecar})
			if !tc.read {
				l := strconv.Itoa(tc.line)
				wantNotes(t, pb.Notes, "warning field_invalid site.argument_specs.yml:"+l, "warning field_invalid roles/r/meta/argument_specs.yml:"+l)
			}
		})
	}
	for name, main := range map[string]string{
		"meta/main argument_specs a list":   "galaxy_info: {}\nargument_specs: [x]\n",
		"meta/main argument_specs a scalar": "galaxy_info: {}\nargument_specs: x\n",
		"meta/main root a list":             "- galaxy_info: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"site.yml": {Data: []byte("- hosts: all\n  roles: [r]\n")}, "roles/r/meta/main.yml": {Data: []byte(main)}}
			pb := ParsePlaybook(fsys, "site.yml")
			wantRoles(t, pb.Roles, RoleUse{"r", "main", RoleUnreadable})
		})
	}
}
