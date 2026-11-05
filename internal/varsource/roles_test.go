package varsource

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRoleResolution(t *testing.T) {
	pb := ParsePlaybook(os.DirFS("testdata/basic"), "sub/play.yml")
	if pb.Failed {
		t.Fatalf("failed (notes %v)", pb.Notes)
	}
	if got, want := fieldNames(pb.Fields), []string{"pre_x", "local_sub", "inblock_x", "inrescue_x", "inalways_x", "post_x", "handler_x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %q, want %q", got, want)
	}
	if f := pb.Fields[1]; f.Source.File != "sub/roles/local/meta/argument_specs.yml" || f.Source.Role != "local" {
		t.Errorf("local_sub source = %+v", f.Source)
	}
	main := func(name string, state RoleState) RoleUse {
		return RoleUse{Name: name, EntryPoint: "main", State: state}
	}
	wantRoles(t, pb.Roles,
		main("pre", RoleRead),
		main("local", RoleRead),
		main("shared", RoleNotFound), // roles/ at the root: not searched for sub/play.yml (backlog 7.90)
		main("missing_role", RoleNotFound),
		main("community.general.thing", RoleCollection),
		main("nospecs", RoleNoSpecs),
		main("inblock", RoleRead),
		main("inrescue", RoleRead),
		main("inalways", RoleRead),
		main("post", RoleRead),
		main("handler", RoleRead),
	)
	wantNotes(t, pb.Notes,
		"warning role_not_found sub/play.yml:7",
		"warning role_not_found sub/play.yml:8",
		"hint role_collection sub/play.yml:9",
		"hint role_without_specs sub/play.yml:10",
	)
}

// oneOption builds a role argument spec with one str option.
func oneOption(name string) string {
	return "argument_specs:\n  main:\n    options:\n      " + name + ":\n        type: str\n"
}

// TestRoleLookupVariants pins the lookup rules beyond the fixture tree:
// path names, the key=value module form, tasks_from, the other spec file
// names Ansible reads, and unreadable spec files.
func TestRoleLookupVariants(t *testing.T) {
	cases := []struct {
		name   string
		play   string
		files  map[string]string
		roles  []RoleUse
		fields []string
		notes  []string
	}{
		{"path relative to the playbook", "  roles: [./roles/x, ../outside]\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"./roles/x", "main", RoleRead}, {"../outside", "main", RoleNotFound}},
			[]string{"x_var"}, []string{"warning role_not_found pb/play.yml:2"}},
		{"path climbing out of the repo", "  roles: [../../etc]\n",
			map[string]string{"etc/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"../../etc", "main", RoleNotFound}}, nil, []string{"warning role_not_found pb/play.yml:2"}},
		{"key=value form", "  tasks:\n    - include_role: name=x tasks_from=setup.yaml\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": "argument_specs:\n  setup:\n    options:\n      s_var: {type: str}\n"},
			[]RoleUse{{"x", "setup", RoleRead}}, []string{"s_var"}, nil},
		{"tasks_from in a directory", "  tasks:\n    - import_role: {name: x, tasks_from: sub/setup.yml}\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": "argument_specs:\n  setup:\n    options:\n      s_var: {type: str}\n"},
			[]RoleUse{{"x", "setup", RoleRead}}, []string{"s_var"}, nil},
		{"templated tasks_from", "  tasks:\n    - import_role: {name: x, tasks_from: \"{{ ep }}\"}\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"x", "{{ ep }}", RoleDynamic}}, nil, []string{"hint role_dynamic pb/play.yml:3"}},
		{"argument_specs.yaml", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yaml": oneOption("x_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
		{"argument_specs.json", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/argument_specs.json": `{"argument_specs": {"main": {"options": {"x_var": {"type": "str"}}}}}`},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
		{"specs in meta/main.yml", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/main.yml": "galaxy_info: {author: me}\n" + oneOption("x_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
		{"meta/main.yml without specs", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/main.yml": "galaxy_info: {author: me}\ndependencies: []\n"},
			[]RoleUse{{"x", "main", RoleNoSpecs}}, nil, []string{"hint role_without_specs pb/play.yml:2"}},
		{"argument_specs.yml wins over meta/main.yml", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": oneOption("a_var"), "pb/roles/x/meta/main.yml": oneOption("m_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"a_var"}, nil},
		{"invalid spec file", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": "argument_specs: [\n"},
			[]RoleUse{{"x", "main", RoleUnreadable}}, nil, []string{"warning invalid_yaml pb/roles/x/meta/argument_specs.yml:1"}},
		{"spec file too large", "  roles: [x]\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": oneOption("x_var") + "#" + strings.Repeat("x", MaxFileBytes)},
			[]RoleUse{{"x", "main", RoleUnreadable}}, nil, []string{"warning file_too_large pb/roles/x/meta/argument_specs.yml:0"}},
		{"entry point missing", "  tasks:\n    - import_role: {name: x, tasks_from: nope}\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"x", "nope", RoleRead}}, nil, []string{"hint entry_point_missing pb/roles/x/meta/argument_specs.yml:1"}},
		{"entry without a role name", "  roles: [{tags: x}]\n  tasks:\n    - include_role: {tasks_from: y}\n", nil,
			[]RoleUse{{"", "main", RoleUnreadable}}, nil, []string{"warning role_invalid pb/play.yml:2", "warning role_invalid pb/play.yml:4"}},
		{"same role and entry point twice", "  roles: [x, {role: x}]\n  tasks:\n    - import_role: {name: x}\n",
			map[string]string{"pb/roles/x/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{"pb/play.yml": {Data: []byte("- hosts: all\n" + tc.play)}}
			for name, data := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(data)}
			}
			pb := ParsePlaybook(fsys, "pb/play.yml")
			if pb.Failed {
				t.Fatalf("failed (notes %v)", pb.Notes)
			}
			wantRoles(t, pb.Roles, tc.roles...)
			if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, append([]string{}, tc.fields...)) {
				t.Errorf("fields = %q, want %q", got, tc.fields)
			}
			wantNotes(t, pb.Notes, tc.notes...)
		})
	}
}

// TestInvalidRoleUsesAreListed: a role use that cannot be read is still a
// RoleUse, unreadable, so the fields of the role it meant are not gone.
func TestInvalidRoleUsesAreListed(t *testing.T) {
	cases := []struct {
		name, play string
		role       RoleUse
		line       int
	}{
		{"role is a list", "  roles:\n    - role: [r]\n", RoleUse{"", "main", RoleUnreadable}, 3},
		{"no name", "  roles:\n    - {tags: x}\n", RoleUse{"", "main", RoleUnreadable}, 3},
		{"tasks_from is a mapping", "  tasks:\n    - include_role: {name: r, tasks_from: {a: b}}\n", RoleUse{"r", "main", RoleUnreadable}, 3},
		{"key=value without name", "  tasks:\n    - include_role: role=r\n", RoleUse{"role=r", "main", RoleUnreadable}, 3},
		{"task without name", "  tasks:\n    - import_role: {tasks_from: main}\n", RoleUse{"", "main", RoleUnreadable}, 3},
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
			wantRoles(t, pb.Roles, tc.role)
			wantNotes(t, pb.Notes, "warning role_invalid site.yml:"+strconv.Itoa(tc.line))
		})
	}
}

// TestDottedRoleNames: a dotted name is looked up like any other (galaxy
// roles committed as roles/<author>.<name>); only a name of three or more
// parts found nowhere is a collection role.
func TestDottedRoleNames(t *testing.T) {
	fsys := fstest.MapFS{
		"site.yml": {Data: []byte("- hosts: all\n  roles: [geerlingguy.nginx, a.b, ns.col.role, ns.col.local, ..]\n")},
		"roles/geerlingguy.nginx/meta/argument_specs.yml": {Data: []byte(oneOption("n_var"))},
		"roles/ns.col.local/meta/argument_specs.yml":      {Data: []byte(oneOption("l_var"))},
		"meta/argument_specs.yml":                         {Data: []byte(oneOption("root_var"))},
	}
	pb := ParsePlaybook(fsys, "site.yml")
	wantRoles(t, pb.Roles,
		RoleUse{"geerlingguy.nginx", "main", RoleRead},
		RoleUse{"a.b", "main", RoleNotFound},
		RoleUse{"ns.col.role", "main", RoleCollection},
		RoleUse{"ns.col.local", "main", RoleRead},
		RoleUse{"..", "main", RoleNotFound},
	)
	if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"n_var", "l_var"}) {
		t.Fatalf("fields = %q", got)
	}
}

// TestRoleSpecParsedOncePerWalk: many entry points of one spec file
// (tasks_from) open and parse it once.
func TestRoleSpecParsedOncePerWalk(t *testing.T) {
	var spec, play strings.Builder
	spec.WriteString("argument_specs:\n")
	play.WriteString("- hosts: all\n  tasks:\n")
	for i := range 50 {
		fmt.Fprintf(&spec, "  e%d:\n    options:\n      v%d: {type: str}\n", i, i)
		fmt.Fprintf(&play, "    - import_role: {name: x, tasks_from: e%d}\n", i)
	}
	c := &countingFS{FS: fstest.MapFS{
		"site.yml":                        {Data: []byte(play.String())},
		"roles/x/meta/argument_specs.yml": {Data: []byte(spec.String())},
	}, opens: map[string]int{}}
	pb := ParsePlaybook(c, "site.yml")
	if len(pb.Roles) != 50 || len(pb.Fields) != 50 {
		t.Fatalf("%d roles, %d fields", len(pb.Roles), len(pb.Fields))
	}
	if n := c.opens["roles/x/meta/argument_specs.yml"]; n != 1 {
		t.Fatalf("the spec file was opened %d times, want once", n)
	}
}
