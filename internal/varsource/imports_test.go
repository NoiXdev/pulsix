package varsource

import (
	"os"
	"reflect"
	"testing"
	"testing/fstest"
)

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
			FileUse{Path: "import_playbook@4", State: FileUnreadable},
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

// TestInvalidImportsAreListed: an import whose target is no text is still
// a FileUse, unreadable, so what it imported is not gone.
func TestInvalidImportsAreListed(t *testing.T) {
	for name, entry := range map[string]string{
		"null":    "- import_playbook:\n",
		"list":    "- import_playbook: [b.yml]\n",
		"mapping": "- import_playbook: {file: b.yml}\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"a.yml": {Data: []byte("- hosts: all\n" + entry)}, "b.yml": {Data: []byte("- hosts: all\n")}}
			pb := ParsePlaybook(fsys, "a.yml")
			wantFiles(t, pb.Files, FileUse{Path: "a.yml", State: FileRead}, FileUse{Path: "import_playbook@2", State: FileUnreadable})
			wantNotes(t, pb.Notes, "warning import_invalid a.yml:2")
		})
	}
}

// TestAbsoluteImportKeepsItsText: an absolute import from a subdirectory
// is listed as written, not as the in-repo path it would join to.
func TestAbsoluteImportKeepsItsText(t *testing.T) {
	fsys := fstest.MapFS{"sub/a.yml": {Data: []byte("- import_playbook: /b.yml\n")}, "sub/b.yml": {Data: []byte("- hosts: all\n")}}
	pb := ParsePlaybook(fsys, "sub/a.yml")
	wantFiles(t, pb.Files, FileUse{Path: "sub/a.yml", State: FileRead}, FileUse{Path: "/b.yml", State: FileOutside})
	wantNotes(t, pb.Notes, "warning import_outside sub/a.yml:1")
}
