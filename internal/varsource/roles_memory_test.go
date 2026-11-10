package varsource

import (
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

// heapFS measures the live heap each time a role spec file is opened: the
// walk is single-threaded, so after runtime.GC() the heap holds what the
// walk kept of the roles it read before (backlog 7.113).
type heapFS struct {
	fs.FS
	peak uint64
}

func (h *heapFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, "argument_specs.yml") {
		h.peak = max(h.peak, liveHeap())
	}
	return h.FS.Open(name)
}

func (h *heapFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(h.FS, name) }

func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// bulkySpec is a valid spec file for entry point main whose size is mostly
// a sequence nothing reads: a parse tree of it is far larger than its bytes.
func bulkySpec(name string, size int) string {
	var b strings.Builder
	b.WriteString("argument_specs:\n  main:\n    options:\n      " + name + ":\n        type: str\npadding: [0")
	for b.Len() < size {
		b.WriteString(",0")
	}
	b.WriteString("]\n")
	return b.String()
}

// TestWalkDoesNotRetainRoleTrees: what a walk keeps per role is what it
// extracted from the spec file, not the parsed tree. A playbook using 31
// roles with bulky spec files holds, when it opens the last role's file, no
// more than a few trees' worth of heap (backlog 7.113; the finding's
// 31 roles of the largest file size, here smaller to keep the test quick).
func TestWalkDoesNotRetainRoleTrees(t *testing.T) {
	if testing.Short() {
		t.Skip("parses 6 MB of YAML under the race detector, about 10 s")
	}
	const roles, size = 31, 200 << 10
	files := fstest.MapFS{}
	var play strings.Builder
	play.WriteString("- hosts: all\n  roles:\n")
	for i := range roles {
		files[fmt.Sprintf("roles/r%d/meta/argument_specs.yml", i)] = &fstest.MapFile{Data: []byte(bulkySpec(fmt.Sprintf("v%d", i), size))}
		fmt.Fprintf(&play, "    - r%d\n", i)
	}
	files["site.yml"] = &fstest.MapFile{Data: []byte(play.String())}

	// One parse: the heap a parsed file holds on its own.
	data := files["roles/r0/meta/argument_specs.yml"].Data
	before := liveHeap()
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	oneParse := liveHeap() - before
	runtime.KeepAlive(&doc)
	if oneParse < uint64(len(data)) {
		t.Fatalf("one parse holds %d bytes for a %d byte file; the measurement is off", oneParse, len(data))
	}

	h := &heapFS{FS: files}
	base := liveHeap()
	pb := ParsePlaybook(h, "site.yml")
	if pb.Failed || len(pb.Fields) != roles {
		t.Fatalf("failed = %v, %d fields, want %d (notes %v)", pb.Failed, len(pb.Fields), roles, pb.Notes)
	}
	held := h.peak - min(h.peak, base)
	t.Logf("one parse %d KiB; heap held while the last role was opened %d KiB", oneParse>>10, held>>10)
	if held >= 3*oneParse {
		t.Fatalf("the walk held %d KiB of heap at its last role, want under 3 x one parse (%d KiB): it keeps parse trees", held>>10, 3*oneParse>>10)
	}
}

// TestRoleEntryPointsAreReplayedPerUse: the entry points of a spec file are
// mapped when it is parsed, but a use gets only its own fields and notes,
// and shows the role name it used (two names may lead to one directory).
func TestRoleEntryPointsAreReplayedPerUse(t *testing.T) {
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

	// A mapping shared between entry points raises its duplicate_key note for
	// whichever of them is used, not only for the first that reads it.
	shared := `argument_specs:
  a:
    options: &O
      x: {type: str}
      x: {type: int}
  b:
    options: *O
`
	pb = ParsePlaybook(fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  tasks:\n    - import_role: {name: x, tasks_from: b}\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(shared)},
	}, "site.yml")
	if codes(pb.Notes)[codeDuplicateKey] != 1 || len(pb.Fields) != 1 {
		t.Fatalf("notes = %v, %d fields; want the duplicate_key warning of the shared mapping", pb.Notes, len(pb.Fields))
	}

	// The same through attrs (an option's spec, an entry point's own keys),
	// which has a cache of its own.
	for name, spec := range map[string]string{
		"option spec": "argument_specs:\n  a:\n    options:\n      o: &S {type: str, type: int}\n  b:\n    options:\n      o: *S\n",
		"entry point": "argument_specs:\n  a: &E\n    options: {o: {type: str}}\n    short_description: x\n    short_description: y\n  b: *E\n",
	} {
		pb = ParsePlaybook(fstest.MapFS{
			"site.yml":                        {Data: []byte("- hosts: all\n  tasks:\n    - import_role: {name: x, tasks_from: b}\n")},
			"roles/x/meta/argument_specs.yml": {Data: []byte(spec)},
		}, "site.yml")
		if codes(pb.Notes)[codeDuplicateKey] != 1 {
			t.Fatalf("%s: notes = %v, want the duplicate_key warning of the shared mapping", name, pb.Notes)
		}
	}

	// A mapping merged into a shared one counts too.
	merged := "argument_specs:\n  a:\n    options: &O\n      <<: &M {x: {type: str}, x: {type: int}}\n  b:\n    options: *O\n"
	pb = ParsePlaybook(fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  tasks:\n    - import_role: {name: x, tasks_from: b}\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(merged)},
	}, "site.yml")
	if codes(pb.Notes)[codeDuplicateKey] != 1 {
		t.Fatalf("merged: notes = %v, want the duplicate_key warning of the merged mapping", pb.Notes)
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

// aliasedEntryPoints is the spec file of the review of backlog 7.113: main
// holds 99 options of 99 sub-options each, and n entry points alias main.
func aliasedEntryPoints(n int) string {
	var b strings.Builder
	b.WriteString("argument_specs:\n  main: &E\n    options:\n      o0: &O\n        type: dict\n        options:\n")
	for i := range 99 {
		fmt.Fprintf(&b, "          l%d: {}\n", i)
	}
	for i := 1; i < 99; i++ {
		fmt.Fprintf(&b, "      o%d: *O\n", i)
	}
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "  e%d: *E\n", i)
	}
	return b.String()
}

// TestAliasedEntryPointsAreBounded: mapping every entry point of a role's
// spec file at parse time costs what the file's budget allows, however many
// entry points alias one big one. The file is then given up as
// file_too_complex, with nothing read from it (backlog 7.113). Before the
// budget was shared, an 11 KB file of this shape took 8.7 GiB of allocation.
func TestAliasedEntryPointsAreBounded(t *testing.T) {
	files := fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  roles: [x]\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(aliasedEntryPoints(800))},
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	pb := ParsePlaybook(files, "site.yml")
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Fatalf("the walk allocated %d MiB, want under 64 MiB", alloc>>20)
	}
	wantRoles(t, pb.Roles, RoleUse{"x", "main", RoleUnreadable})
	if pb.Failed || len(pb.Fields) != 0 {
		t.Fatalf("failed = %v, %d fields; want none from a file given up", pb.Failed, len(pb.Fields))
	}
	wantNotes(t, pb.Notes, "warning file_too_complex roles/x/meta/argument_specs.yml:0")
}

// TestEntryPointsShareTheOptionBudget: entry points that each fit the
// budget but not together give the file up whole, whichever is used; one
// within it is read.
func TestEntryPointsShareTheOptionBudget(t *testing.T) {
	spec := func(entryPoints, options int) string {
		var b strings.Builder
		b.WriteString("argument_specs:\n")
		for e := range entryPoints {
			fmt.Fprintf(&b, "  e%d:\n    options:\n      o: {type: dict, options: {s: {type: str}", e)
			for o := range options {
				fmt.Fprintf(&b, ", s%d: {type: str}", o)
			}
			b.WriteString("}}\n")
		}
		return b.String()
	}
	run := func(spec string) Playbook {
		return ParsePlaybook(fstest.MapFS{
			"site.yml":                        {Data: []byte("- hosts: all\n  tasks:\n    - import_role: {name: x, tasks_from: e0}\n")},
			"roles/x/meta/argument_specs.yml": {Data: []byte(spec)},
		}, "site.yml")
	}
	if pb := run(spec(3, maxSpecFields/4)); len(pb.Fields) != 1 || pb.Roles[0].State != RoleRead {
		t.Fatalf("within the budget: %d fields, roles %v, notes %v", len(pb.Fields), pb.Roles, pb.Notes)
	}
	pb := run(spec(5, maxSpecFields/4))
	wantRoles(t, pb.Roles, RoleUse{"x", "e0", RoleUnreadable})
	if len(pb.Fields) != 0 {
		t.Fatalf("%d fields from a file given up", len(pb.Fields))
	}
	wantNotes(t, pb.Notes, "warning file_too_complex roles/x/meta/argument_specs.yml:0")
}

// TestSpecsMappingNotesAreRaisedOnce: the duplicate keys of the
// argument_specs mapping are noted once, so they do not push the notes of
// the entry point in use past the file's cap.
func TestSpecsMappingNotesAreRaisedOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("argument_specs:\n  main:\n    options:\n      o: {type: raw}\n")
	for range 150 {
		b.WriteString("  other: {}\n")
	}
	pb := ParsePlaybook(fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  roles: [x]\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(b.String())},
	}, "site.yml")
	counts := codes(pb.Notes)
	if counts[codeUnsupportedType] != 1 || counts[codeTooManyNotes] != 0 {
		t.Fatalf("notes = %v, want the unsupported_type warning and no too_many_notes", counts)
	}
}

// TestSharedDuplicateNotesAreBounded: entry points that merge one mapping
// with many duplicate keys re-raise its notes for each of them. Doing that
// is charged to the file and keeps no copy of the notes, so ten thousand
// such entry points give the file up instead of holding gigabytes (backlog
// 7.113).
func TestSharedDuplicateNotesAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("d: &D {")
	for range 201 {
		b.WriteString("z: 1, ")
	}
	b.WriteString("}\nargument_specs:\n")
	for i := range 9_990 {
		fmt.Fprintf(&b, "  e%d: {<<: *D}\n", i)
	}
	h := &heapFS{FS: fstest.MapFS{
		"site.yml":                        {Data: []byte("- hosts: all\n  roles: [x, y]\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(b.String())},
		"roles/y/meta/argument_specs.yml": {Data: []byte(oneOption("y_var"))},
	}}
	base := liveHeap()
	pb := ParsePlaybook(h, "site.yml")
	held := h.peak - min(h.peak, base)
	t.Logf("heap held when the next role was opened: %d KiB", held>>10)
	if held > 16<<20 {
		t.Fatalf("the walk held %d MiB when it opened the next role, want under 16 MiB", held>>20)
	}
	wantRoles(t, pb.Roles, RoleUse{"x", "main", RoleUnreadable}, RoleUse{"y", "main", RoleRead})
	if got := fieldNames(pb.Fields); !slices.Equal(got, []string{"y_var"}) {
		t.Fatalf("fields = %q, want only y's", got)
	}
	wantNotes(t, pb.Notes, "warning file_too_complex roles/x/meta/argument_specs.yml:0")
}
