package varsource

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// countingFS counts the opens of each name.
type countingFS struct {
	fs.FS
	opens map[string]int
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.opens[name]++
	return c.FS.Open(name)
}

func codes(notes []Note) map[string]int {
	out := map[string]int{}
	for _, n := range notes {
		out[n.Code]++
	}
	return out
}

// TestRoleReadOncePerWalk: a role used many times (through aliases, from
// tasks and from an imported playbook) is resolved and read once.
func TestRoleReadOncePerWalk(t *testing.T) {
	src := "- hosts: all\n  roles: [&r x" + strings.Repeat(", *r", 1000) + "]\n  tasks:\n    - import_role: {name: x}\n- import_playbook: other.yml\n"
	c := &countingFS{FS: fstest.MapFS{
		"site.yml":                        {Data: []byte(src)},
		"other.yml":                       {Data: []byte("- hosts: all\n  roles: [x]\n")},
		"roles/x/meta/argument_specs.yml": {Data: []byte(oneOption("x_var"))},
	}, opens: map[string]int{}}
	pb := ParsePlaybook(c, "site.yml")
	if pb.Failed || len(pb.Roles) != 1 || len(pb.Fields) != 1 {
		t.Fatalf("failed = %v, roles %v, %d fields", pb.Failed, pb.Roles, len(pb.Fields))
	}
	if n := c.opens["roles/x/meta/argument_specs.yml"]; n != 1 {
		t.Fatalf("the spec file was opened %d times, want once", n)
	}
}

// TestAliasedWalksAreCharged: a play, a vars mapping or a task list
// reached through many aliases costs its size on every visit, so a small
// file cannot make the walk loop for free.
func TestAliasedWalksAreCharged(t *testing.T) {
	cases := map[string]string{
		"plays":       "- &p {hosts: all, vars: " + wideMapping(20_000, "a: 1") + "}\n" + strings.Repeat("- *p\n", 60),
		"role params": "- hosts: all\n  roles:\n    - &r " + wideMapping(20_000, "role: x") + "\n" + strings.Repeat("    - *r\n", 60),
		"tasks":       "- hosts: all\n  tasks: &t\n" + strings.Repeat("    - debug: msg=x\n", 20_000) + "- hosts: all\n  tasks:\n" + strings.Repeat("    - block: *t\n", 60),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(src)}}, "site.yml")
			if !pb.Failed {
				t.Fatalf("read with %d fields; want the file given up", len(pb.Fields))
			}
			wantNotes(t, pb.Notes, "warning file_too_complex site.yml:0")
		})
	}
}

func TestDeepBlocksGiveUpTheFile(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  tasks:\n")
	indent := "    "
	for range 70 {
		b.WriteString(indent + "- block:\n")
		indent += "    "
	}
	b.WriteString(indent + "- include_role: {name: x}\n")
	pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(b.String())}}, "site.yml")
	if !pb.Failed {
		t.Fatalf("read; want the file given up")
	}
	wantNotes(t, pb.Notes, "warning file_too_complex site.yml:0")
}

// TestWalkCaps: one playbook with its imports and roles reads at most
// maxWalkFiles playbook files, maxWalkRoles roles and maxWalkBytes bytes;
// past a cap the playbook counts as not read.
func TestWalkCaps(t *testing.T) {
	t.Run("roles", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("- hosts: all\n  roles:\n")
		for i := range maxWalkRoles + 1 {
			fmt.Fprintf(&b, "    - r%d\n", i)
		}
		pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(b.String())}}, "site.yml")
		if !pb.Failed || codes(pb.Notes)[codePlaybookTooComplex] != 1 {
			t.Fatalf("failed = %v, notes %v", pb.Failed, codes(pb.Notes))
		}
	})
	t.Run("files", func(t *testing.T) {
		fsys := fstest.MapFS{}
		var b strings.Builder
		for i := range maxWalkFiles {
			fmt.Fprintf(&b, "- import_playbook: p%d.yml\n", i)
			fsys[fmt.Sprintf("p%d.yml", i)] = &fstest.MapFile{Data: []byte("- hosts: all\n")}
		}
		fsys["site.yml"] = &fstest.MapFile{Data: []byte(b.String())}
		pb := ParsePlaybook(fsys, "site.yml")
		if !pb.Failed || codes(pb.Notes)[codePlaybookTooComplex] != 1 {
			t.Fatalf("failed = %v, notes %v", pb.Failed, codes(pb.Notes))
		}
	})
	t.Run("steps", func(t *testing.T) {
		// Each file walks one 20 000-key mapping 45 times: under the
		// file's budget, past the walk's after five files.
		fsys := fstest.MapFS{}
		var b strings.Builder
		heavy := "- &p {hosts: all, vars: " + wideMapping(20_000, "a: 1") + "}\n" + strings.Repeat("- *p\n", 44)
		for i := range maxWalkSteps/maxVisits + 1 {
			fmt.Fprintf(&b, "- import_playbook: p%d.yml\n", i)
			fsys[fmt.Sprintf("p%d.yml", i)] = &fstest.MapFile{Data: []byte(heavy)}
		}
		fsys["site.yml"] = &fstest.MapFile{Data: []byte(b.String())}
		pb := ParsePlaybook(fsys, "site.yml")
		if !pb.Failed || codes(pb.Notes)[codePlaybookTooComplex] != 1 || codes(pb.Notes)[codeFileTooComplex] != 0 {
			t.Fatalf("failed = %v, notes %v", pb.Failed, codes(pb.Notes))
		}
	})
	t.Run("bytes", func(t *testing.T) {
		fsys := fstest.MapFS{}
		var b strings.Builder
		pad := "- hosts: all\n#" + strings.Repeat("x", MaxFileBytes-100) + "\n"
		for i := range maxWalkBytes/MaxFileBytes + 1 {
			fmt.Fprintf(&b, "- import_playbook: p%d.yml\n", i)
			fsys[fmt.Sprintf("p%d.yml", i)] = &fstest.MapFile{Data: []byte(pad)}
		}
		fsys["site.yml"] = &fstest.MapFile{Data: []byte(b.String())}
		pb := ParsePlaybook(fsys, "site.yml")
		if !pb.Failed || codes(pb.Notes)[codePlaybookTooComplex] != 1 {
			t.Fatalf("failed = %v, notes %v", pb.Failed, codes(pb.Notes))
		}
	})
}

// TestNotesPerFileAreCapped: one file contributes at most maxNotesPerFile
// notes, then one too_many_notes warning counting the rest.
func TestNotesPerFileAreCapped(t *testing.T) {
	src := "- hosts: all\n  vars:\n" + strings.Repeat("    k: 1\n", 301)
	pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(src)}}, "site.yml")
	got := codes(pb.Notes)
	if got[codeDuplicateKey] != maxNotesPerFile || got[codeTooManyNotes] != 1 || len(pb.Notes) != maxNotesPerFile+1 {
		t.Fatalf("notes %v, want %d duplicate_key and one too_many_notes", got, maxNotesPerFile)
	}
	last := pb.Notes[len(pb.Notes)-1]
	if last.Code != codeTooManyNotes || last.File != "site.yml" || !strings.Contains(last.Message, "100") {
		t.Fatalf("last note = %+v, want too_many_notes on site.yml counting 100", last)
	}
}

func TestCapNotes(t *testing.T) {
	var notes []Note
	for i := range 1500 {
		n := warning(codeDuplicateKey, "a.yml", i, "x")
		notes = append(notes, n, n) // every note twice
	}
	got := capNotes(notes, "site.yml", maxNotesPerList)
	if len(got) != maxNotesPerList+1 {
		t.Fatalf("%d notes, want %d and one summary", len(got), maxNotesPerList)
	}
	for i, n := range got[:maxNotesPerList] {
		if n.Line != i {
			t.Fatalf("note %d has line %d: repeated notes are not dropped in order", i, n.Line)
		}
	}
	if last := got[maxNotesPerList]; last.Code != codeTooManyNotes || last.File != "site.yml" || !strings.Contains(last.Message, "500") {
		t.Fatalf("summary = %+v", last)
	}
	if few := capNotes(notes[:4], "site.yml", maxNotesPerList); len(few) != 2 {
		t.Fatalf("%d notes, want the 2 distinct ones", len(few))
	}
}

func TestMergedFieldCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  vars:\n")
	for i := range 102 {
		fmt.Fprintf(&b, "    v%d: 1  # pulsix:\n", i)
	}
	pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(b.String())}}, "site.yml")
	if pb.Failed || len(pb.Fields) != 100 || pb.Fields[99].Name != "v99" {
		t.Fatalf("failed = %v, %d fields", pb.Failed, len(pb.Fields))
	}
	wantNotes(t, pb.Notes, "warning too_many_fields site.yml:0")
	if !strings.Contains(pb.Notes[0].Message, "v100") {
		t.Fatalf("message %q does not name the first field left out", pb.Notes[0].Message)
	}
}

func TestSetInPlaybookListIsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  vars:\n")
	for i := range maxListedSetNames + 1 {
		fmt.Fprintf(&b, "    v%04d: 1\n", i)
	}
	pb := ParsePlaybook(fstest.MapFS{"site.yml": {Data: []byte(b.String())}}, "site.yml")
	if len(pb.SetInPlaybook) != maxListedSetNames || pb.SetInPlaybook[0] != "v0000" {
		t.Fatalf("%d names listed, first %q", len(pb.SetInPlaybook), pb.SetInPlaybook[0])
	}
	wantNotes(t, pb.Notes, "hint too_many_set_names site.yml:0")
}
