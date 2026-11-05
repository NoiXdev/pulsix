package varsource

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func fieldNames(fields []schema.Field) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

func wantRoles(t *testing.T, got []RoleUse, want ...RoleUse) {
	t.Helper()
	if len(want) == 0 {
		want = []RoleUse{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roles =\n%+v\nwant\n%+v", got, want)
	}
}

func wantFiles(t *testing.T, got []FileUse, want ...FileUse) {
	t.Helper()
	if len(want) == 0 {
		want = []FileUse{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files =\n%+v\nwant\n%+v", got, want)
	}
}

func argSpec(role, entryPoint, file string, line int, f schema.Field) schema.Field {
	f.Origin = schema.OriginSource
	f.Source = &schema.FieldSource{Kind: schema.SourceArgumentSpec, Role: role, EntryPoint: entryPoint, File: file, Line: line}
	return f
}

func TestParsePlaybookBasic(t *testing.T) {
	pb := ParsePlaybook(os.DirFS("testdata/basic"), "site.yml")
	if pb.Failed || pb.Path != "site.yml" {
		t.Fatalf("failed = %v, path = %q (notes %v)", pb.Failed, pb.Path, pb.Notes)
	}
	nginx := "roles/nginx/meta/argument_specs.yml"
	app := "roles/app/meta/argument_specs.yml"
	wantFields(t, pb.Fields, []schema.Field{
		{Name: "release", Type: schema.TypeString, Required: true, Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceSidecar, EntryPoint: "main", File: "site.argument_specs.yml", Line: 4}},
		{Name: "http_port", Type: schema.TypeInt, Required: true, Label: "HTTP port", Default: 8080, Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceMarker, File: "site.yml", Line: 3}},
		{Name: "admin_user", Type: schema.TypeString, Required: true, Label: "Admin user", Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceVarsPrompt, File: "site.yml", Line: 6}},
		argSpec("nginx", "main", nginx, 5, schema.Field{Name: "nginx_port", Type: schema.TypeInt, Default: 80}),
		argSpec("nginx", "main", nginx, 8, schema.Field{Name: "nginx_mode", Type: schema.TypeEnum, Options: []string{"blue", "green"}}),
		argSpec("nginx", "main", nginx, 10, schema.Field{Name: "app_env", Type: schema.TypeString, Required: true, SetInPlaybook: true}),
		argSpec("app", "main", app, 4, schema.Field{Name: "app_port", Type: schema.TypeInt, SetInPlaybook: true}),
		argSpec("app", "main", app, 6, schema.Field{Name: "app_secret", Type: schema.TypeSecret}),
		argSpec("db", "install", "roles/db/meta/argument_specs.yml", 8, schema.Field{Name: "db_path", Type: schema.TypeString}),
	})
	if !reflect.DeepEqual(pb.SetInPlaybook, []string{"app_env", "app_port"}) {
		t.Errorf("set in playbook = %q", pb.SetInPlaybook)
	}
	wantRoles(t, pb.Roles,
		RoleUse{Name: "nginx", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "app", EntryPoint: "main", State: RoleRead},
		RoleUse{Name: "db", EntryPoint: "install", State: RoleRead},
		RoleUse{Name: "{{ role_name }}", EntryPoint: "main", State: RoleDynamic},
	)
	wantFiles(t, pb.Files,
		FileUse{Path: "site.yml", State: FileRead},
		FileUse{Path: "site.argument_specs.yml", State: FileRead},
	)
	wantNotes(t, pb.Notes,
		"hint role_dynamic site.yml:20",
		"hint duplicate_name "+nginx+":13",
	)
}

func TestPrecedenceFirstWins(t *testing.T) {
	pb := ParsePlaybook(os.DirFS("testdata/basic"), "site.yml")
	release := pb.Fields[0]
	if release.Name != "release" || release.Source.Kind != schema.SourceSidecar || release.Label != "" {
		t.Fatalf("release = %+v, want the sidecar's version", release)
	}
	var dups []Note
	for _, n := range pb.Notes {
		if n.Code == codeDuplicateName {
			dups = append(dups, n)
		}
	}
	if len(dups) != 1 {
		t.Fatalf("duplicate_name notes = %v, want one", dups)
	}
	n := dups[0]
	if n.Level != LevelHint || n.File != "roles/nginx/meta/argument_specs.yml" || n.Line != 13 || !strings.Contains(n.Message, "site.argument_specs.yml:4") {
		t.Fatalf("duplicate_name = %+v, want the nginx declaration at line 13 naming the sidecar's place", n)
	}
}

func TestRoleResolution(t *testing.T) {
	pb := ParsePlaybook(os.DirFS("testdata/basic"), "sub/play.yml")
	if pb.Failed {
		t.Fatalf("failed (notes %v)", pb.Notes)
	}
	if got, want := fieldNames(pb.Fields), []string{"pre_x", "local_sub", "shared_x", "inblock_x", "inrescue_x", "inalways_x", "post_x", "handler_x"}; !reflect.DeepEqual(got, want) {
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
		main("shared", RoleRead),
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
			map[string]string{"roles/x/meta/argument_specs.yml": "argument_specs:\n  setup:\n    options:\n      s_var: {type: str}\n"},
			[]RoleUse{{"x", "setup", RoleRead}}, []string{"s_var"}, nil},
		{"tasks_from in a directory", "  tasks:\n    - import_role: {name: x, tasks_from: sub/setup.yml}\n",
			map[string]string{"roles/x/meta/argument_specs.yml": "argument_specs:\n  setup:\n    options:\n      s_var: {type: str}\n"},
			[]RoleUse{{"x", "setup", RoleRead}}, []string{"s_var"}, nil},
		{"templated tasks_from", "  tasks:\n    - import_role: {name: x, tasks_from: \"{{ ep }}\"}\n",
			map[string]string{"roles/x/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"x", "{{ ep }}", RoleDynamic}}, nil, []string{"hint role_dynamic pb/play.yml:3"}},
		{"argument_specs.yaml", "  roles: [x]\n",
			map[string]string{"roles/x/meta/argument_specs.yaml": oneOption("x_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
		{"argument_specs.json", "  roles: [x]\n",
			map[string]string{"roles/x/meta/argument_specs.json": `{"argument_specs": {"main": {"options": {"x_var": {"type": "str"}}}}}`},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
		{"specs in meta/main.yml", "  roles: [x]\n",
			map[string]string{"roles/x/meta/main.yml": "galaxy_info: {author: me}\n" + oneOption("x_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"x_var"}, nil},
		{"meta/main.yml without specs", "  roles: [x]\n",
			map[string]string{"roles/x/meta/main.yml": "galaxy_info: {author: me}\ndependencies: []\n"},
			[]RoleUse{{"x", "main", RoleNoSpecs}}, nil, []string{"hint role_without_specs pb/play.yml:2"}},
		{"argument_specs.yml wins over meta/main.yml", "  roles: [x]\n",
			map[string]string{"roles/x/meta/argument_specs.yml": oneOption("a_var"), "roles/x/meta/main.yml": oneOption("m_var")},
			[]RoleUse{{"x", "main", RoleRead}}, []string{"a_var"}, nil},
		{"invalid spec file", "  roles: [x]\n",
			map[string]string{"roles/x/meta/argument_specs.yml": "argument_specs: [\n"},
			[]RoleUse{{"x", "main", RoleUnreadable}}, nil, []string{"warning invalid_yaml roles/x/meta/argument_specs.yml:1"}},
		{"spec file too large", "  roles: [x]\n",
			map[string]string{"roles/x/meta/argument_specs.yml": oneOption("x_var") + "#" + strings.Repeat("x", MaxFileBytes)},
			[]RoleUse{{"x", "main", RoleUnreadable}}, nil, []string{"warning file_too_large roles/x/meta/argument_specs.yml:0"}},
		{"entry point missing", "  tasks:\n    - import_role: {name: x, tasks_from: nope}\n",
			map[string]string{"roles/x/meta/argument_specs.yml": oneOption("x_var")},
			[]RoleUse{{"x", "nope", RoleRead}}, nil, []string{"hint entry_point_missing roles/x/meta/argument_specs.yml:1"}},
		{"entry without a role name", "  roles: [{tags: x}]\n  tasks:\n    - include_role: {tasks_from: y}\n", nil,
			nil, nil, []string{"warning role_invalid pb/play.yml:2", "warning role_invalid pb/play.yml:4"}},
		{"same role and entry point twice", "  roles: [x, {role: x}]\n  tasks:\n    - import_role: {name: x}\n",
			map[string]string{"roles/x/meta/argument_specs.yml": oneOption("x_var")},
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

func TestImports(t *testing.T) {
	imports := os.DirFS("testdata/imports")
	t.Run("order, cycle, outside, dynamic, missing, set in playbook", func(t *testing.T) {
		pb := ParsePlaybook(imports, "a.yml")
		if pb.Failed {
			t.Fatalf("failed (notes %v)", pb.Notes)
		}
		if got, want := fieldNames(pb.Fields), []string{"a_marked", "ra_var", "b_marked"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("fields = %q, want %q", got, want)
		}
		if !pb.Fields[1].SetInPlaybook || pb.Fields[0].SetInPlaybook || pb.Fields[2].SetInPlaybook {
			t.Errorf("set in playbook flags = %v %v %v, want only ra_var (set in b.yml)", pb.Fields[0].SetInPlaybook, pb.Fields[1].SetInPlaybook, pb.Fields[2].SetInPlaybook)
		}
		if pb.Fields[2].Source.File != "b.yml" || pb.Fields[2].Source.Line != 4 {
			t.Errorf("b_marked source = %+v", pb.Fields[2].Source)
		}
		wantFiles(t, pb.Files,
			FileUse{Path: "a.yml", State: FileRead},
			FileUse{Path: "b.yml", State: FileRead},
			FileUse{Path: "a.yml", State: FileCycle},
			FileUse{Path: "../x.yml", State: FileOutside},
			FileUse{Path: "{{ x }}.yml", State: FileDynamic},
			FileUse{Path: "nope.yml", State: FileMissing},
		)
		wantRoles(t, pb.Roles, RoleUse{Name: "ra", EntryPoint: "main", State: RoleRead})
		wantNotes(t, pb.Notes,
			"warning import_outside a.yml:7",
			"hint import_dynamic a.yml:8",
			"warning import_not_found a.yml:9",
			"warning import_cycle b.yml:5",
		)
	})
	t.Run("depth", func(t *testing.T) {
		pb := ParsePlaybook(imports, "d1.yml")
		if got, want := fieldNames(pb.Fields), []string{"d1_var", "d2_var", "d3_var", "d4_var"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("fields = %q, want %q", got, want)
		}
		wantFiles(t, pb.Files,
			FileUse{Path: "d1.yml", State: FileRead},
			FileUse{Path: "d2.yml", State: FileRead},
			FileUse{Path: "d3.yml", State: FileRead},
			FileUse{Path: "d4.yml", State: FileRead},
			FileUse{Path: "d5.yml", State: FileTooDeep},
		)
		wantNotes(t, pb.Notes, "warning import_too_deep d4.yml:4")
	})
	t.Run("imported files that are not read", func(t *testing.T) {
		fsys := fstest.MapFS{
			"site.yml":       {Data: []byte("- import_playbook: plays/bad.yml\n- import_playbook: plays/list.yml\n- import_playbook: plays/a.yml\n- import_playbook: [x]\n- import_playbook: sp ace.yml\n- import_playbook: my.coll.play\n")},
			"plays/bad.yml":  {Data: []byte("- hosts: [\n")},
			"plays/list.yml": {Data: []byte("- debug: msg=x\n")},
			"plays/a.yml":    {Data: []byte("- import_playbook: b.yml\n")},
			"plays/b.yml":    {Data: []byte("- hosts: all\n  vars:\n    b_var: 1  # pulsix:\n")},
		}
		pb := ParsePlaybook(fsys, "site.yml")
		if pb.Failed {
			t.Fatalf("failed (notes %v)", pb.Notes)
		}
		if got := fieldNames(pb.Fields); !reflect.DeepEqual(got, []string{"b_var"}) {
			t.Errorf("fields = %q, want b_var (paths relative to the importing file)", got)
		}
		wantFiles(t, pb.Files,
			FileUse{Path: "site.yml", State: FileRead},
			FileUse{Path: "plays/bad.yml", State: FileUnreadable},
			FileUse{Path: "plays/list.yml", State: FileUnreadable},
			FileUse{Path: "plays/a.yml", State: FileRead},
			FileUse{Path: "plays/b.yml", State: FileRead},
			FileUse{Path: "sp ace.yml", State: FileUnreadable},
			FileUse{Path: "my.coll.play", State: FileOutside},
		)
		wantNotes(t, pb.Notes,
			"warning import_invalid site.yml:4",
			"warning path_unsupported site.yml:5",
			"warning import_outside site.yml:6",
			"warning invalid_yaml plays/bad.yml:1",
			"warning not_a_playbook plays/list.yml:0",
		)
	})
}

func TestParsePlaybookFailures(t *testing.T) {
	big := "- hosts: all\n#" + strings.Repeat("x", 600<<10) + "\n"
	cases := []struct {
		name  string
		path  string
		data  string
		state FileState
		note  string
	}{
		{"invalid YAML", "site.yml", "- hosts: all\n  vars: [x\n", FileUnreadable, "warning invalid_yaml site.yml:1"},
		{"too large", "site.yml", big, FileUnreadable, "warning file_too_large site.yml:0"},
		{"not a playbook", "site.yml", "key: value\n", FileUnreadable, "warning not_a_playbook site.yml:0"},
		{"a play without hosts", "site.yml", "- hosts: all\n- name: x\n", FileUnreadable, "warning not_a_playbook site.yml:0"},
		{"missing", "nope.yml", "", FileMissing, "warning read_failed nope.yml:0"},
		{"path unsupported", "../site.yml", "", FileUnreadable, "warning path_unsupported ../site.yml:0"},
		{"too complex", "site.yml", "- &p {hosts: all, vars: " + wideMapping(20_000, "a: 1") + "}\n" + strings.Repeat("- *p\n", 60), FileUnreadable, "warning file_too_complex site.yml:0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{"site.yml": {Data: []byte(tc.data)}}
			pb := ParsePlaybook(fsys, tc.path)
			if !pb.Failed || len(pb.Fields) != 0 {
				t.Fatalf("failed = %v, %d fields; want failed without fields", pb.Failed, len(pb.Fields))
			}
			wantFiles(t, pb.Files, FileUse{Path: tc.path, State: tc.state})
			wantNotes(t, pb.Notes, tc.note)
		})
	}
}

func TestMarkerMisplaced(t *testing.T) {
	src := `- hosts: all
  vars:
    ok_var: 1  # pulsix: OK
  tasks:
    - debug: msg=hi
      vars:
        task_var: 1  # pulsix: Misplaced
    - import_role:
        name: x
      vars:
        role_var: 2  # pulsix: Also misplaced
- import_playbook: other.yml
  vars:
    imp_var: 3  # pulsix: Misplaced too
`
	fsys := fstest.MapFS{
		"site.yml":                        {Data: []byte(src)},
		"other.yml":                       {Data: []byte("- hosts: all\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(oneOption("task_var") + "      role_var: {type: str}\n      imp_var: {type: str}\n")},
	}
	pb := ParsePlaybook(fsys, "site.yml")
	if got, want := fieldNames(pb.Fields), []string{"ok_var", "task_var", "role_var", "imp_var"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %q, want %q", got, want)
	}
	for _, f := range pb.Fields[1:] {
		if f.Source.Kind != schema.SourceArgumentSpec {
			t.Errorf("%s is a %s field, want only the role's", f.Name, f.Source.Kind)
		}
	}
	if !reflect.DeepEqual(pb.SetInPlaybook, []string{"imp_var", "role_var"}) {
		t.Errorf("set in playbook = %q, want the import's and the role task's vars", pb.SetInPlaybook)
	}
	wantNotes(t, pb.Notes,
		"warning marker_misplaced site.yml:7",
		"warning marker_misplaced site.yml:11",
		"warning marker_misplaced site.yml:14",
	)
}

// TestMarkedNameIsNeverSetInPlaybook: a marker on a name in one play wins
// over the same name set plainly in another, or passed to a role.
func TestMarkedNameIsNeverSetInPlaybook(t *testing.T) {
	src := "- hosts: all\n  vars:\n    port: 1\n  roles: [{role: x, port: 2}]\n- hosts: all\n  vars:\n    port: 3  # pulsix:\n"
	fsys := fstest.MapFS{
		"site.yml":                        {Data: []byte(src)},
		"roles/x/meta/argument_specs.yml": {Data: []byte(oneOption("port"))},
	}
	pb := ParsePlaybook(fsys, "site.yml")
	if len(pb.Fields) != 1 || pb.Fields[0].Source.Kind != schema.SourceMarker || pb.Fields[0].SetInPlaybook {
		t.Fatalf("fields = %+v, want the marker field, not set in playbook", pb.Fields)
	}
	if len(pb.SetInPlaybook) != 0 {
		t.Fatalf("set in playbook = %q, want none", pb.SetInPlaybook)
	}
}

// TestSidecar: the sidecar is <name>.argument_specs.yml or .yaml next to
// the playbook; one that cannot be read is listed unreadable, so its fields
// are not taken as gone.
func TestSidecar(t *testing.T) {
	play := &fstest.MapFile{Data: []byte("- hosts: all\n")}
	cases := []struct {
		name  string
		file  string
		data  string
		state FileState
		notes []string
	}{
		{"yaml extension", "deploy.argument_specs.yaml", oneOption("s_var"), FileRead, nil},
		{"invalid", "deploy.argument_specs.yml", "argument_specs: [\n", FileUnreadable, []string{"warning invalid_yaml deploy.argument_specs.yml:1"}},
		{"a directory", "deploy.argument_specs.yml/x", "", FileUnreadable, []string{"warning read_failed deploy.argument_specs.yml:0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pb := ParsePlaybook(fstest.MapFS{"deploy.yaml": play, tc.file: {Data: []byte(tc.data)}}, "deploy.yaml")
			sidecar := strings.TrimSuffix(tc.file, "/x")
			wantFiles(t, pb.Files, FileUse{Path: "deploy.yaml", State: FileRead}, FileUse{Path: sidecar, State: tc.state})
			if tc.state == FileRead && (len(pb.Fields) != 1 || pb.Fields[0].Source.Kind != schema.SourceSidecar) {
				t.Errorf("fields = %+v, want the sidecar's s_var", pb.Fields)
			}
			wantNotes(t, pb.Notes, tc.notes...)
		})
	}
}
