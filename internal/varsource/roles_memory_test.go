package varsource

import (
	"fmt"
	"io/fs"
	"runtime"
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
	if want := []string{"a_var@x/main", "b_var@./roles/x/other"}; !equalStrings(got, want) {
		t.Fatalf("fields = %q, want %q", got, want)
	}
	counts := codes(pb.Notes)
	if counts[codeDuplicateKey] != 1 || counts[codeEntryPointMissing] != 1 {
		t.Fatalf("notes = %v", pb.Notes)
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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
