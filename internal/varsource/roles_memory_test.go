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
// walk kept of the roles it read before (backlog 7.113). It also counts the
// opens of each name.
type heapFS struct {
	fs.FS
	peak  uint64
	opens map[string]int
}

func (h *heapFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, "argument_specs.yml") {
		h.peak = max(h.peak, liveHeap())
	}
	if h.opens == nil {
		h.opens = map[string]int{}
	}
	h.opens[name]++
	return h.FS.Open(name)
}

func (h *heapFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(h.FS, name) }

func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// heldBy walks the playbook site.yml of files and returns the result and
// the heap the walk held, at most, when it opened a role spec file.
func heldBy(files fstest.MapFS) (Playbook, *heapFS, uint64) {
	h := &heapFS{FS: files}
	base := liveHeap()
	pb := ParsePlaybook(h, "site.yml")
	return pb, h, h.peak - min(h.peak, base)
}

// rolesUsing is n roles r0… sharing one spec file, each used for the
// comma-separated entry points of use, in that order.
func rolesUsing(spec string, n int, use string) fstest.MapFS {
	files := fstest.MapFS{}
	var play strings.Builder
	play.WriteString("- hosts: all\n  tasks:\n")
	for i := range n {
		files[fmt.Sprintf("roles/r%d/meta/argument_specs.yml", i)] = &fstest.MapFile{Data: []byte(spec)}
		for _, ep := range strings.Split(use, ",") {
			fmt.Fprintf(&play, "    - import_role: {name: r%d, tasks_from: %s}\n", i, ep)
		}
	}
	files["site.yml"] = &fstest.MapFile{Data: []byte(play.String())}
	return files
}

func wantAllRead(t *testing.T, pb Playbook, n int) {
	t.Helper()
	if pb.Failed || len(pb.Roles) != n {
		t.Fatalf("failed = %v, %d roles, want %d read (notes %v)", pb.Failed, len(pb.Roles), n, pb.Notes)
	}
	for _, r := range pb.Roles {
		if r.State != RoleRead {
			t.Fatalf("role %v, want read", r)
		}
	}
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

	pb, _, held := heldBy(files)
	if pb.Failed || len(pb.Fields) != roles {
		t.Fatalf("failed = %v, %d fields, want %d (notes %v)", pb.Failed, len(pb.Fields), roles, pb.Notes)
	}
	t.Logf("one parse %d KiB; heap held while the last role was opened %d KiB", oneParse>>10, held>>10)
	if held >= 3*oneParse {
		t.Fatalf("the walk held %d KiB of heap at its last role, want under 3 x one parse (%d KiB): it keeps parse trees", held>>10, 3*oneParse>>10)
	}
}

// TestWalkDoesNotRetainImportTrees: an imported playbook's tree goes once
// the file and what it pulls in are read; the walk keeps its notes only.
// When the last of ten bulky imports opens its role's spec file, the walk
// holds that import's tree, not all ten (backlog 7.113).
func TestWalkDoesNotRetainImportTrees(t *testing.T) {
	if testing.Short() {
		t.Skip("parses 2 MB of YAML under the race detector")
	}
	const imports, size = 10, 200 << 10
	play := func(roles string) string {
		var b strings.Builder
		b.WriteString("- hosts: all\n  roles: [" + roles + "]\n  vars:\n    padding: [0")
		for b.Len() < size {
			b.WriteString(",0")
		}
		b.WriteString("]\n")
		return b.String()
	}
	files := fstest.MapFS{"roles/x/meta/argument_specs.yml": {Data: []byte(oneOption("x_var"))}}
	var site strings.Builder
	for i := range imports {
		roles := ""
		if i == imports-1 {
			roles = "x"
		}
		files[fmt.Sprintf("p%d.yml", i)] = &fstest.MapFile{Data: []byte(play(roles))}
		fmt.Fprintf(&site, "- import_playbook: p%d.yml\n", i)
	}
	files["site.yml"] = &fstest.MapFile{Data: []byte(site.String())}

	before := liveHeap()
	var doc yaml.Node
	if err := yaml.Unmarshal(files["p0.yml"].Data, &doc); err != nil {
		t.Fatal(err)
	}
	oneParse := liveHeap() - before
	runtime.KeepAlive(&doc)

	pb, _, held := heldBy(files)
	if pb.Failed || len(pb.Fields) != 1 {
		t.Fatalf("failed = %v, %d fields (notes %v)", pb.Failed, len(pb.Fields), pb.Notes)
	}
	t.Logf("one parse %d KiB; heap held when the role was opened %d KiB", oneParse>>10, held>>10)
	if held >= 3*oneParse {
		t.Fatalf("the walk held %d KiB at the last import's role, want under 3 x one parse (%d KiB): it keeps import trees", held>>10, 3*oneParse>>10)
	}
}

// dupMapping is a flow mapping of n keys, all the same.
func dupMapping(n int) string {
	return "{" + strings.TrimSuffix(strings.Repeat("z,", n), ",") + "}"
}

// TestUnusedEntryPointsCostNothing: a spec file's entry points no use asks
// for are not mapped, so nothing of them is kept, and the shapes that
// made mapping them all expensive read as they always did (backlog 7.113).
func TestUnusedEntryPointsCostNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("parses about 6 MB of YAML under the race detector")
	}
	const limit = 16 << 20

	t.Run("entry points of many duplicate keys", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("argument_specs:\n  main: {}\n")
		for i := range 1200 {
			fmt.Fprintf(&b, "  e%d: %s\n", i, dupMapping(201))
		}
		pb, _, held := heldBy(rolesUsing(b.String(), 6, "main"))
		t.Logf("held %d KiB", held>>10)
		if held > limit {
			t.Fatalf("the walk held %d MiB at its last role, want under %d MiB", held>>20, limit>>20)
		}
		wantAllRead(t, pb, 6)
		wantNotes(t, pb.Notes)
	})

	t.Run("merges of mappings with duplicate keys", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("d: [")
		for i := range 1200 {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "&m%d %s", i, dupMapping(201))
		}
		b.WriteString("]\n")
		var groups []string
		for g := range 19 {
			var refs []string
			for i := g * 64; i < min(1200, (g+1)*64); i++ {
				refs = append(refs, fmt.Sprintf("*m%d", i))
			}
			fmt.Fprintf(&b, "g%d: &g%d {<<: [%s]}\n", g, g, strings.Join(refs, ", "))
			groups = append(groups, fmt.Sprintf("*g%d", g))
		}
		// e0 and main both merge the 1 200 mappings; both are used.
		merge := "{<<: [" + strings.Join(groups, ", ") + "]}"
		b.WriteString("argument_specs:\n  e0: " + merge + "\n  main: " + merge + "\n")
		pb, _, held := heldBy(rolesUsing(b.String(), 6, "e0,main"))
		t.Logf("held %d KiB", held>>10)
		if held > limit {
			t.Fatalf("the walk held %d MiB at its last role, want under %d MiB", held>>20, limit>>20)
		}
		wantAllRead(t, pb, 12)
		// Each file notes its 1 200 x 200 duplicate keys once, for e0: 200
		// kept.
		var want []string
		for i := range 6 {
			file := fmt.Sprintf("roles/r%d/meta/argument_specs.yml", i)
			want = append(want, "warning duplicate_key "+file+":1", "warning too_many_notes "+file+":0")
		}
		wantNotes(t, pb.Notes, want...)
		if m := pb.Notes[1].Message; m != "239800 more notes are left out" {
			t.Fatalf("too_many_notes says %q", m)
		}
	})

	t.Run("nested merges of small mappings with duplicate keys", func(t *testing.T) {
		// main merges 19 x 64 x 64 = 77 824 inline mappings {z,z} through
		// nested merge lists; what a file keeps of which of them were noted
		// stays small.
		const roles = 15
		leaf := "{<<: [" + strings.TrimSuffix(strings.Repeat(dupMapping(2)+",", 64), ",") + "]}"
		mid := "{<<: [" + strings.TrimSuffix(strings.Repeat(leaf+",", 64), ",") + "]}"
		spec := "argument_specs:\n  main: {<<: [" + strings.TrimSuffix(strings.Repeat(mid+",", 19), ",") + "]}\n"
		pb, _, held := heldBy(rolesUsing(spec, roles, "main"))
		t.Logf("held %d KiB", held>>10)
		if held > limit {
			t.Fatalf("the walk held %d MiB at its last role, want under %d MiB", held>>20, limit>>20)
		}
		wantAllRead(t, pb, roles)
		var want []string
		for i := range roles {
			file := fmt.Sprintf("roles/r%d/meta/argument_specs.yml", i)
			want = append(want, "warning duplicate_key "+file+":2", "warning too_many_notes "+file+":0")
		}
		wantNotes(t, pb.Notes, want...)
		if m := pb.Notes[1].Message; m != "77624 more notes are left out" {
			t.Fatalf("too_many_notes says %q", m)
		}
	})

	t.Run("entry points aliasing a shared options mapping", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("o: &O\n")
		for i := range 100 {
			fmt.Fprintf(&b, "  v%d: {type: str, description: d}\n", i)
		}
		b.WriteString("argument_specs:\n")
		for i := range 99 {
			fmt.Fprintf(&b, "  e%d: {options: *O}\n", i)
		}
		b.WriteString("  main: {options: {}}\n")
		pb, _, held := heldBy(rolesUsing(b.String(), 500, "main"))
		t.Logf("held %d KiB", held>>10)
		if held > limit {
			t.Fatalf("the walk held %d MiB at its last role, want under %d MiB", held>>20, limit>>20)
		}
		wantAllRead(t, pb, 500)
		wantNotes(t, pb.Notes)
	})

	t.Run("entry points aliasing a big one", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("argument_specs:\n  main: &E\n    options:\n      o0: &O\n        type: dict\n        options:\n")
		for i := range 99 {
			fmt.Fprintf(&b, "          l%d: {}\n", i)
		}
		for i := 1; i < 99; i++ {
			fmt.Fprintf(&b, "      o%d: *O\n", i)
		}
		for i := 1; i <= 800; i++ {
			fmt.Fprintf(&b, "  e%d: *E\n", i)
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		pb := ParsePlaybook(rolesUsing(b.String(), 1, "main"), "site.yml")
		runtime.ReadMemStats(&after)
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
			t.Fatalf("the walk allocated %d MiB, want under 64 MiB", alloc>>20)
		}
		wantAllRead(t, pb, 1)
		if len(pb.Fields) != 99 || len(pb.Fields[0].Fields) != 99 {
			t.Fatalf("%d fields, want main's 99 of 99 sub-options each (notes %v)", len(pb.Fields), pb.Notes)
		}
	})
}

// TestSharedDuplicateKeysKeepNoUncappedList: a mapping with a quarter of a
// million duplicate keys keeps no more of its notes than the file's cap,
// however many roles read such files, and the count of the notes left out
// stays exact (backlog 7.113).
func TestSharedDuplicateKeysKeepNoUncappedList(t *testing.T) {
	if testing.Short() {
		t.Skip("parses 3 MB of YAML under the race detector")
	}
	const keys, roles = 250_000, 6
	spec := "d: &D " + dupMapping(keys) + "\nargument_specs:\n  e0: {<<: *D}\n  main: {<<: *D}\n"
	pb, _, held := heldBy(rolesUsing(spec, roles, "main"))
	t.Logf("heap held when the last role was opened: %d KiB", held>>10)
	if held > 16<<20 {
		t.Fatalf("the walk held %d MiB at its last role, want under 16 MiB", held>>20)
	}
	wantAllRead(t, pb, roles)
	// All but the first of the equal keys are noted; the file keeps 200.
	want := fmt.Sprintf("%d more notes are left out", keys-1-maxNotesPerFile)
	found := false
	for _, n := range pb.Notes {
		found = found || (n.Code == codeTooManyNotes && n.Message == want)
	}
	if !found {
		t.Fatalf("no too_many_notes note saying %q in %d notes", want, len(pb.Notes))
	}
}
