package varsource

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// TestRoleEntryPointsAreReadPerUse: a use gets only its own entry point's
// fields and notes, and shows the role name it used (two names may lead to
// one directory).
func TestRoleEntryPointsAreReadPerUse(t *testing.T) {
	spec := `argument_specs:
  main:
    options:
      a_var: {type: str}
  other:
    options:
      b_var: {type: str}
      b_var: {type: int}
`
	play := "- hosts: all\n  tasks:\n    - import_role: {name: x}\n    - import_role: {name: ./roles/x, tasks_from: other}\n    - import_role: {name: x, tasks_from: nope}\n"
	pb := ParsePlaybook(fstest.MapFS{
		"site.yml":                        {Data: []byte(play)},
		"roles/x/meta/argument_specs.yml": {Data: []byte(spec)},
	}, "site.yml")
	var got []string
	for _, f := range pb.Fields {
		got = append(got, f.Name+"@"+f.Source.Role+"/"+f.Source.EntryPoint)
	}
	if want := []string{"a_var@x/main", "b_var@./roles/x/other"}; !slices.Equal(got, want) {
		t.Fatalf("fields = %q, want %q", got, want)
	}
	counts := codes(pb.Notes)
	if counts[codeDuplicateKey] != 1 || counts[codeEntryPointMissing] != 1 {
		t.Fatalf("notes = %v", pb.Notes)
	}

	// A mapping shared between entry points raises its duplicate_key note
	// for whichever of them is used, whether read through pairs or attrs,
	// or merged into the shared mapping.
	for name, spec := range map[string]string{
		"options":     "argument_specs:\n  a:\n    options: &O\n      x: {type: str}\n      x: {type: int}\n  b:\n    options: *O\n",
		"option spec": "argument_specs:\n  a:\n    options:\n      o: &S {type: str, type: int}\n  b:\n    options:\n      o: *S\n",
		"entry point": "argument_specs:\n  a: &E\n    options: {o: {type: str}}\n    short_description: x\n    short_description: y\n  b: *E\n",
		"merged":      "argument_specs:\n  a:\n    options: &O\n      <<: &M {x: {type: str}, x: {type: int}}\n  b:\n    options: *O\n",
	} {
		pb = ParsePlaybook(fstest.MapFS{
			"site.yml":                        {Data: []byte("- hosts: all\n  tasks:\n    - import_role: {name: x, tasks_from: b}\n")},
			"roles/x/meta/argument_specs.yml": {Data: []byte(spec)},
		}, "site.yml")
		if codes(pb.Notes)[codeDuplicateKey] != 1 || len(pb.Fields) != 1 {
			t.Fatalf("%s: notes = %v, %d fields; want the duplicate_key warning of the shared mapping", name, pb.Notes, len(pb.Fields))
		}
	}

	// An entry point no use asks for raises no note.
	pb = ParsePlaybook(fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  roles: [x]\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(spec)},
	}, "site.yml")
	if len(pb.Notes) != 0 {
		t.Fatalf("notes of an unused entry point: %v", pb.Notes)
	}
}

// TestSecondEntryPointRereadsTheFile: the tree of a spec file is let go
// after each use, so a second entry point reads and parses the file again,
// once; the result is what one parse for both gave (backlog 7.113).
func TestSecondEntryPointRereadsTheFile(t *testing.T) {
	spec := `argument_specs:
  main: &M
    options:
      a_var: {type: str}
      a_var: {type: int}
      odd: {type: raw}
  other:
    options:
      <<: *M
      b_var: {type: str, description: [x, y]}
`
	files := rolesUsing(spec, 2, "main,other,nope,main")
	files["site.yml"].Data = append(files["site.yml"].Data, "    - import_role: {name: ./roles/r0, tasks_from: other}\n"...)
	pb, h, _ := heldBy(files)
	for i := range 2 {
		if n := h.opens[fmt.Sprintf("roles/r%d/meta/argument_specs.yml", i)]; n != 3 {
			t.Fatalf("r%d's spec file was opened %d times, want once per entry point (3)", i, n)
		}
	}
	wantRoles(t, pb.Roles,
		RoleUse{"r0", "main", RoleRead}, RoleUse{"r0", "other", RoleRead}, RoleUse{"r0", "nope", RoleRead},
		RoleUse{"r1", "main", RoleRead}, RoleUse{"r1", "other", RoleRead}, RoleUse{"r1", "nope", RoleRead})
	if got := fieldNames(pb.Fields); !slices.Equal(got, []string{"a_var", "options", "b_var"}) {
		t.Fatalf("fields = %q", got)
	}
	r0, r1 := "roles/r0/meta/argument_specs.yml", "roles/r1/meta/argument_specs.yml"
	wantNotes(t, pb.Notes,
		// The duplicate key of main is noted once per file, although the
		// second parse reads it again for other.
		"warning duplicate_key "+r0+":5", "warning unsupported_type "+r0+":6", "hint entry_point_missing "+r0+":1",
		"warning duplicate_key "+r1+":5", "warning unsupported_type "+r1+":6", "hint entry_point_missing "+r1+":1",
		"hint duplicate_name "+r1+":5", "hint duplicate_name "+r1+":3", "hint duplicate_name "+r1+":10")
}

// TestRereadsAreCharged: reading a spec file again for another entry point
// costs the walk what the first read did: its bytes, and the steps of
// what it maps (backlog 7.113).
func TestRereadsAreCharged(t *testing.T) {
	// 40 entry points of a 420 KiB file read 16.4 MiB, past the walk's
	// 16 MiB.
	var big strings.Builder
	big.WriteString("argument_specs:\n")
	var eps []string
	for i := range 40 {
		fmt.Fprintf(&big, "  e%d: {}\n", i)
		eps = append(eps, fmt.Sprintf("e%d", i))
	}
	big.WriteString("padding: " + strings.Repeat("x", 420<<10) + "\n")
	pb := ParsePlaybook(rolesUsing(big.String(), 1, strings.Join(eps, ",")), "site.yml")
	if !pb.Failed || len(pb.Notes) != 1 || pb.Notes[0].Code != codePlaybookTooComplex || !strings.Contains(pb.Notes[0].Message, "MiB") {
		t.Fatalf("failed = %v, notes %v; want the walk given up on its bytes", pb.Failed, pb.Notes)
	}

	// The argument_specs mapping merges a 10 000-key mapping 64 times:
	// 650 000 steps for each read of it, so the second entry point's read
	// takes the file past its 1 000 000.
	var wide strings.Builder
	wide.WriteString("d: &D {e0: {}, e1: {}")
	for i := range 10_000 {
		fmt.Fprintf(&wide, ", k%d: 0", i)
	}
	wide.WriteString("}\nargument_specs: {<<: [*D" + strings.Repeat(", *D", 63) + "]}\n")
	pb = ParsePlaybook(rolesUsing(wide.String(), 1, "e0,e1"), "site.yml")
	wantRoles(t, pb.Roles, RoleUse{"r0", "e0", RoleRead}, RoleUse{"r0", "e1", RoleUnreadable})
	wantNotes(t, pb.Notes, "warning file_too_complex roles/r0/meta/argument_specs.yml:0")
}

// TestOneEntryPointOverTheFileBudget: an entry point whose own mapping
// passes the file's budget on the first read leaves the file exhausted. Its
// uses report unreadable, the file is noted once, and the later entry
// point does not open it again (backlog 7.121).
func TestOneEntryPointOverTheFileBudget(t *testing.T) {
	// One mapping merges the 10 000-key one 64 times and argument_specs
	// merges that 64 times (the most merge sources of a mapping): about
	// 1 300 000 steps, past the file's 1 000 000 on the first read.
	var wide strings.Builder
	wide.WriteString("d: &D {e0: {}, e1: {}")
	for i := range 10_000 {
		fmt.Fprintf(&wide, ", k%d: 0", i)
	}
	wide.WriteString("}\nm: &M {<<: [*D" + strings.Repeat(", *D", 63) + "]}\n")
	wide.WriteString("argument_specs: {<<: [*M" + strings.Repeat(", *M", 63) + "]}\n")
	pb, h, _ := heldBy(rolesUsing(wide.String(), 1, "e0,e1"))
	file := "roles/r0/meta/argument_specs.yml"
	wantRoles(t, pb.Roles, RoleUse{"r0", "e0", RoleUnreadable}, RoleUse{"r0", "e1", RoleUnreadable})
	wantNotes(t, pb.Notes, "warning file_too_complex "+file+":0")
	if n := h.opens[file]; n != 1 {
		t.Fatalf("the spec file was opened %d times, want once", n)
	}
}

// TestRepeatedEntryPointIsNotReread: a use of an entry point already
// mapped is served from what the first use kept. Two entry point names
// longer than a shown name, alike in what is shown, are such a pair: the
// second adds the same notes and duplicate_name hints as before, without
// opening the file again (backlog 7.113).
func TestRepeatedEntryPointIsNotReread(t *testing.T) {
	long := strings.Repeat("e", 300)
	shown := clip(long, maxRefBytes)
	spec := "argument_specs:\n  " + shown + ":\n    options:\n      c_var: {type: str}\n      c_odd: {type: raw}\n      c_dup: {type: str}\n      c_dup: {type: str}\n"
	pb, h, _ := heldBy(rolesUsing(spec, 1, long+","+long+"x,"+shown+",main"))
	file := "roles/r0/meta/argument_specs.yml"
	if n := h.opens[file]; n != 2 {
		t.Fatalf("the spec file was opened %d times, want twice (the long name, main)", n)
	}
	wantRoles(t, pb.Roles, RoleUse{"r0", shown, RoleRead}, RoleUse{"r0", shown, RoleRead}, RoleUse{"r0", shown, RoleRead}, RoleUse{"r0", "main", RoleRead})
	if got := fieldNames(pb.Fields); !slices.Equal(got, []string{"c_var", "c_dup"}) {
		t.Fatalf("fields = %q", got)
	}
	wantNotes(t, pb.Notes, "warning duplicate_key "+file+":7", "warning unsupported_type "+file+":5", "hint entry_point_missing "+file+":1",
		"hint duplicate_name "+file+":4", "hint duplicate_name "+file+":7")
}

// TestSpecsMappingNotesAreRaisedOnce: the duplicate keys of the
// argument_specs mapping are noted once per file, however many entry
// points read it, so they do not push the notes of the entry points in use
// past the file's cap.
func TestSpecsMappingNotesAreRaisedOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("argument_specs:\n  main:\n    options:\n      o: {type: raw}\n")
	for range 150 {
		b.WriteString("  other: {}\n")
	}
	pb := ParsePlaybook(rolesUsing(b.String(), 1, "main,other"), "site.yml")
	counts := codes(pb.Notes)
	if counts[codeDuplicateKey] != 149 || counts[codeUnsupportedType] != 1 || counts[codeTooManyNotes] != 0 {
		t.Fatalf("notes = %v, want 149 duplicate_key, the unsupported_type warning and no too_many_notes", counts)
	}
}

// TestSharedDuplicateKeysAreCountedOnce: a mapping with many duplicate
// keys merged into two entry points, both used, notes its keys once: the
// count of the notes left out is that of one read (backlog 7.113).
func TestSharedDuplicateKeysAreCountedOnce(t *testing.T) {
	spec := "d: &D " + dupMapping(1001) + "\nargument_specs:\n  a: {<<: *D, options: {a_var: {type: str}}}\n  b: {<<: *D, options: {b_var: {type: raw}}}\n"
	pb, h, _ := heldBy(rolesUsing(spec, 1, "a,b"))
	file := "roles/r0/meta/argument_specs.yml"
	if n := h.opens[file]; n != 2 {
		t.Fatalf("the spec file was opened %d times, want twice", n)
	}
	wantNotes(t, pb.Notes, "warning duplicate_key "+file+":1", "warning too_many_notes "+file+":0")
	// 1 000 duplicate keys and b_var's unsupported_type, 200 kept.
	if m := pb.Notes[1].Message; m != "801 more notes are left out" {
		t.Fatalf("too_many_notes says %q", m)
	}
}

// TestDuplicateNotesOfMappingsAtOnePlace: a mapping used as a key starts
// where the root mapping does. Each notes its duplicate keys, also when
// the second is read only by a later parse of the file (backlog 7.113).
func TestDuplicateNotesOfMappingsAtOnePlace(t *testing.T) {
	spec := "&k {a: 1, a: 2}: v\nx: 1\nx: 2\nargument_specs: {a: {options: {o: {type: str}}}, b: {options: {p: {type: str}}, <<: *k}}\n"
	pb := ParsePlaybook(rolesUsing(spec, 1, "a,b"), "site.yml")
	file := "roles/r0/meta/argument_specs.yml"
	wantNotes(t, pb.Notes, "warning duplicate_key "+file+":3", "warning duplicate_key "+file+":1")
	if got := fieldNames(pb.Fields); !slices.Equal(got, []string{"o", "p"}) {
		t.Fatalf("fields = %q", got)
	}
}
