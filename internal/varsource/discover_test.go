package varsource

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func playbookPaths(r Report) []string {
	paths := make([]string, len(r.Playbooks))
	for i, pb := range r.Playbooks {
		paths[i] = pb.Path
	}
	return paths
}

func TestDiscover(t *testing.T) {
	r := ParseRepo(context.Background(), os.DirFS("testdata/basic"), "abc123")
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
	r := ParseRepo(context.Background(), fsys, "")
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
	r := ParseRepo(context.Background(), fsys, "c")
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
	r := ParseRepo(context.Background(), fsys, "c")
	if len(r.Notes) != maxNotesPerList+1 || r.Notes[maxNotesPerList].Code != codeTooManyNotes {
		t.Fatalf("%d notes, want %d and one too_many_notes", len(r.Notes), maxNotesPerList)
	}
}

// cancellingFS cancels its context when a file named stopAt is opened and
// records every YAML file opened.
type cancellingFS struct {
	fstest.MapFS
	stopAt string
	cancel func()
	opened []string
}

func (c *cancellingFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".yml") {
		c.opened = append(c.opened, name)
	}
	if name == c.stopAt {
		c.cancel()
	}
	return c.MapFS.Open(name)
}

func TestParseRepoStopsWhenTheContextEnds(t *testing.T) {
	play := &fstest.MapFile{Data: []byte("- hosts: all\n")}
	files := fstest.MapFS{
		"p0.yml":         play,
		"p1.yml":         play,
		"p2.yml":         play,
		"p3.yml":         play,
		"q.yml":          {Data: []byte("a: 1\n")}, // not looked at once stopped: a name that may be a playbook
		"with space.yml": play,                     // no playbook path: not listed
	}
	t.Run("midway", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		fsys := &cancellingFS{MapFS: files, stopAt: "p1.yml", cancel: func() { cancel(context.DeadlineExceeded) }}
		r := ParseRepo(ctx, fsys, "c")
		// The playbook being read when the time ran out is read to its end.
		if got, want := playbookPaths(r), []string{"p0.yml", "p1.yml"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("playbooks = %q, want %q", got, want)
		}
		if want := []string{"p2.yml", "p3.yml", "q.yml"}; !r.Incomplete || !reflect.DeepEqual(r.LeftOut, want) {
			t.Fatalf("incomplete %v, left out %q, want %q", r.Incomplete, r.LeftOut, want)
		}
		wantNotes(t, r.Notes, "warning time_limit :0")
		if !strings.Contains(r.Notes[0].Message, "p2.yml") {
			t.Fatalf("message %q does not name p2.yml", r.Notes[0].Message)
		}
		for _, name := range fsys.opened {
			if strings.HasPrefix(name, "p2") || strings.HasPrefix(name, "p3") || name == "q.yml" {
				t.Fatalf("%s was opened after the stop; opened %q", name, fsys.opened)
			}
		}
	})
	t.Run("before the first file", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()
		fsys := &cancellingFS{MapFS: files, cancel: cancel}
		r := ParseRepo(ctx, fsys, "c")
		if len(r.Playbooks) != 0 || len(fsys.opened) != 0 {
			t.Fatalf("playbooks %q, opened %q", playbookPaths(r), fsys.opened)
		}
		if want := []string{"p0.yml", "p1.yml", "p2.yml", "p3.yml", "q.yml"}; !r.Incomplete || !reflect.DeepEqual(r.LeftOut, want) {
			t.Fatalf("incomplete %v, left out %q", r.Incomplete, r.LeftOut)
		}
		wantNotes(t, r.Notes, "warning time_limit :0")
	})
	t.Run("cancelled", func(t *testing.T) {
		// A read cancelled by its caller says so; it claims no time limit.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := ParseRepo(ctx, &cancellingFS{MapFS: files, cancel: cancel}, "c")
		if len(r.Playbooks) != 0 || !r.Incomplete || len(r.LeftOut) != 5 {
			t.Fatalf("report = %+v", r)
		}
		wantNotes(t, r.Notes, "warning read_cancelled :0")
	})
}

// failingRootFS cannot list its root.
type failingRootFS struct{}

func (failingRootFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestParseRepoMarksAnUnreadRepoFailed(t *testing.T) {
	r := ParseRepo(context.Background(), failingRootFS{}, "c")
	if !r.Failed || len(r.Playbooks) != 0 || r.Commit != "c" {
		t.Fatalf("report = %+v", r)
	}
	wantNotes(t, r.Notes, "warning read_failed :0")
	if r := ParseRepo(context.Background(), fstest.MapFS{}, "c"); r.Failed {
		t.Fatal("an empty repository is read, not failed")
	}
}

func TestParseRepoStopsPastTheCapWithoutReading(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := range MaxPlaybooks + 2 {
		fsys[fmt.Sprintf("p%04d.yml", i)] = &fstest.MapFile{Data: []byte("- hosts: all\n")}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := &cancellingFS{MapFS: fsys, stopAt: fmt.Sprintf("p%04d.yml", MaxPlaybooks), cancel: func() { cancel(context.DeadlineExceeded) }}
	r := ParseRepo(ctx, stop, "c")
	capped, stopped := fmt.Sprintf("p%04d.yml", MaxPlaybooks), fmt.Sprintf("p%04d.yml", MaxPlaybooks+1)
	if len(r.Playbooks) != MaxPlaybooks || !reflect.DeepEqual(r.LeftOut, []string{capped, stopped}) {
		t.Fatalf("%d playbooks, left out %q", len(r.Playbooks), r.LeftOut)
	}
	wantNotes(t, r.Notes, "warning too_many_playbooks :0", "warning time_limit :0")
	if opened := stop.opened[len(stop.opened)-1]; opened != capped {
		t.Fatalf("last file opened %s, want %s (the one past the stop is not looked at)", opened, capped)
	}
}

func TestReadFailed(t *testing.T) {
	n := ReadFailed("the commit could not be determined")
	if n != (Note{Level: LevelWarning, Code: codeReadFailed, Message: "the commit could not be determined"}) {
		t.Fatalf("note = %+v", n)
	}
}
