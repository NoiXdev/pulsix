package requirements

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFind(t *testing.T) {
	file := &fstest.MapFile{Data: []byte("- src: ./r.tar.gz\n")}
	dir := &fstest.MapFile{Mode: fs.ModeDir | 0o755}
	cases := []struct {
		name    string
		clone   fstest.MapFS
		subpath string
		want    Files
	}{
		{
			name:  "none",
			clone: fstest.MapFS{"site.yml": file},
			want:  Files{},
		},
		{
			name:  "root file without a subpath",
			clone: fstest.MapFS{"requirements.yml": file},
			want:  Files{Root: "", Paths: []string{"requirements.yml"}},
		},
		{
			name:    "the subpath wins over the root",
			clone:   fstest.MapFS{"ansible/requirements.yml": file, "requirements.yml": file},
			subpath: "ansible",
			want:    Files{Root: "ansible", Paths: []string{"ansible/requirements.yml"}},
		},
		{
			name:    "the root when the subpath has none",
			clone:   fstest.MapFS{"ansible/site.yml": file, "requirements.yml": file},
			subpath: "ansible",
			want:    Files{Root: "", Paths: []string{"requirements.yml"}},
		},
		{
			name: "all three in goal 3 order",
			clone: fstest.MapFS{
				"collections/requirements.yml": file,
				"roles/requirements.yml":       file,
				"requirements.yml":             file,
			},
			want: Files{Paths: []string{
				"requirements.yml", "roles/requirements.yml", "collections/requirements.yml",
			}},
		},
		{
			name:  "only .yaml",
			clone: fstest.MapFS{"requirements.yaml": file},
			want:  Files{Paths: []string{"requirements.yaml"}},
		},
		{
			name:  ".yml before .yaml",
			clone: fstest.MapFS{"requirements.yaml": file, "requirements.yml": file},
			want:  Files{Paths: []string{"requirements.yml"}},
		},
		{
			name:    "the subpath holding only collections/requirements.yaml",
			clone:   fstest.MapFS{"ansible/collections/requirements.yaml": file, "requirements.yml": file},
			subpath: "ansible",
			want:    Files{Root: "ansible", Paths: []string{"ansible/collections/requirements.yaml"}},
		},
		{
			name:  "a directory is not counted",
			clone: fstest.MapFS{"requirements.yml": dir, "requirements.yml/x": file},
			want:  Files{},
		},
		{
			name:  "a .yml directory falls through to .yaml",
			clone: fstest.MapFS{"roles/requirements.yml/x": file, "roles/requirements.yaml": file},
			want:  Files{Paths: []string{"roles/requirements.yaml"}},
		},
		{
			name:    "a subpath with a trailing slash is the same root",
			clone:   fstest.MapFS{"ansible/requirements.yml": file},
			subpath: "ansible/",
			want:    Files{Root: "ansible", Paths: []string{"ansible/requirements.yml"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Find(tc.clone, tc.subpath)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Find = %+v, want %+v", got, tc.want)
			}
			if got.Empty() != (len(tc.want.Paths) == 0) {
				t.Fatalf("Empty() = %v for %+v", got.Empty(), got)
			}
		})
	}
}

// statErrFS answers Stat for one path with an error and everything else as
// not existing.
type statErrFS struct {
	path string
	err  error
}

func (f statErrFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (f statErrFS) Stat(name string) (fs.FileInfo, error) {
	if name == f.path {
		return nil, f.err
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func TestFindReportsAStatError(t *testing.T) {
	_, err := Find(statErrFS{path: "requirements.yml", err: errors.New("outside the repository's files")}, "")
	if err == nil {
		t.Fatal("Find: no error")
	}
	for _, want := range []string{"requirements.yml", "outside the repository's files"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q does not contain %q", err, want)
		}
	}
}

func TestFindReportsAStatErrorInTheSubpath(t *testing.T) {
	_, err := Find(statErrFS{path: "ansible/roles/requirements.yaml", err: errors.New("boom")}, "ansible")
	if err == nil || !strings.Contains(err.Error(), "ansible/roles/requirements.yaml: boom") {
		t.Fatalf("err = %v, want the path and the cause", err)
	}
}

// A file where a counted location expects a directory (a file named roles)
// means nothing can be there: not counted, not an error.
func TestFindOnAnOSRootTreatsAFileInThePathAsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "roles"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.yml"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	got, err := Find(root.FS().(fs.StatFS), "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !reflect.DeepEqual(got.Paths, []string{"requirements.yml"}) {
		t.Fatalf("Paths = %v", got.Paths)
	}
}

// The confined root refuses a symlink that leaves the clone; Find reports
// it instead of following it or treating it as missing.
func TestFindOnAnOSRootRefusesALinkOutOfTheClone(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "requirements.yml"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "requirements.yml"), filepath.Join(dir, "requirements.yml")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	_, err = Find(root.FS().(fs.StatFS), "")
	if err == nil || !strings.HasPrefix(err.Error(), "requirements.yml: ") {
		t.Fatalf("err = %v, want a refusal naming requirements.yml", err)
	}
}
