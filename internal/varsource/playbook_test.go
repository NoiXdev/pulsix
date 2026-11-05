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
