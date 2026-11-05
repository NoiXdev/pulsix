package varsource

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func playbookPaths(r Report) []string {
	paths := make([]string, len(r.Playbooks))
	for i, pb := range r.Playbooks {
		paths[i] = pb.Path
	}
	return paths
}

func TestDiscover(t *testing.T) {
	r := ParseRepo(os.DirFS("testdata/basic"), "abc123")
	if r.Commit != "abc123" || r.Incomplete || len(r.LeftOut) != 0 {
		t.Fatalf("commit %q, incomplete %v, left out %v", r.Commit, r.Incomplete, r.LeftOut)
	}
	if got, want := playbookPaths(r), []string{"site.yml", "sub/play.yml"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("playbooks = %q, want %q", got, want)
	}
	if got := fieldNames(r.Playbooks[0].Fields); len(got) != 9 {
		t.Fatalf("site.yml fields = %q, want the nine of TestParsePlaybookBasic", got)
	}
	wantNotes(t, r.Notes,
		"warning invalid_yaml bad.yml:1",
		"warning path_unsupported with space.yml:0",
	)
}

func TestDiscoverSkipsAndSizes(t *testing.T) {
	play := &fstest.MapFile{Data: []byte("- hosts: all\n")}
	fsys := fstest.MapFS{
		"a.yaml":                       play,
		"b.YML":                        play,
		"c.yml.j2":                     play,
		"deep/x/requirements.yaml":     play,
		"deep/x/p.argument_specs.yaml": play,
		"deep/x/p.yml":                 play,
		"tasks.yml":                    {Data: []byte("# tasks\n- debug: msg=x\n")},
		"k8s.yml":                      {Data: []byte("apiVersion: v1\nkind: [\n")},
		"big.yml":                      {Data: []byte("- hosts: all\n#" + strings.Repeat("x", MaxFileBytes) + "\n")},
		"bigmap.yml":                   {Data: []byte("a: 1\n#" + strings.Repeat("x", MaxFileBytes) + "\n")},
		"doc.yml":                      {Data: []byte("%YAML 1.1\n--- # plays\n- hosts: all\n")},
		"inline.yml":                   {Data: []byte("--- [{hosts: all}]\n")},
	}
	r := ParseRepo(fsys, "")
	if got, want := playbookPaths(r), []string{"a.yaml", "deep/x/p.yml", "doc.yml", "inline.yml"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("playbooks = %q, want %q", got, want)
	}
	wantNotes(t, r.Notes, "warning file_too_large big.yml:0")
}

func TestParseRepoCapsPlaybooks(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := range MaxPlaybooks + 1 {
		fsys[fmt.Sprintf("p%04d.yml", i)] = &fstest.MapFile{Data: []byte("- hosts: all\n")}
	}
	fsys["zz.yml"] = &fstest.MapFile{Data: []byte("a: 1\n")} // no playbook: not left out
	r := ParseRepo(fsys, "c")
	if len(r.Playbooks) != MaxPlaybooks || !r.Incomplete {
		t.Fatalf("%d playbooks, incomplete %v", len(r.Playbooks), r.Incomplete)
	}
	last := fmt.Sprintf("p%04d.yml", MaxPlaybooks)
	if !reflect.DeepEqual(r.LeftOut, []string{last}) {
		t.Fatalf("left out = %q, want %q", r.LeftOut, last)
	}
	wantNotes(t, r.Notes, "warning too_many_playbooks :0")
	if !strings.Contains(r.Notes[0].Message, last) {
		t.Fatalf("message %q does not name %s", r.Notes[0].Message, last)
	}
}

func TestParseRepoCapsItsNotes(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := range maxNotesPerList + 5 {
		fsys[fmt.Sprintf("bad%04d.yml", i)] = &fstest.MapFile{Data: []byte("- [\n")}
	}
	r := ParseRepo(fsys, "c")
	if len(r.Notes) != maxNotesPerList+1 || r.Notes[maxNotesPerList].Code != codeTooManyNotes {
		t.Fatalf("%d notes, want %d and one too_many_notes", len(r.Notes), maxNotesPerList)
	}
}
