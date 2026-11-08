package requirements

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// refusingFS is a clone whose confined root refuses vendor/link (a link
// out of the clone) and git's own directory, as the worker's clone FS does.
type refusingFS struct{ fstest.MapFS }

func (f refusingFS) Stat(name string) (fs.FileInfo, error) {
	for _, refused := range []string{"vendor/link", ".git"} {
		if name == refused || strings.HasPrefix(name, refused+"/") {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: errors.New("outside the repository's files")}
		}
	}
	return f.MapFS.Stat(name)
}

// statOnlyFS hides ReadLink and Lstat: a clone FS that cannot tell a link.
type statOnlyFS struct{ fsys fs.StatFS }

func (f statOnlyFS) Open(name string) (fs.File, error)     { return f.fsys.Open(name) }
func (f statOnlyFS) Stat(name string) (fs.FileInfo, error) { return f.fsys.Stat(name) }

const testCloneDir = "/cache/org/repo"

func briefClone() refusingFS {
	return refusingFS{fstest.MapFS{
		"vendor/r.tar.gz":     {Data: []byte("tar")},
		"vendor/c/galaxy.yml": {Data: []byte("namespace: ns\n")},
	}}
}

func link(target string) *fstest.MapFile {
	return &fstest.MapFile{Mode: fs.ModeSymlink | 0o777, Data: []byte(target)}
}

// linkClone holds links inside and out of the clone, and the requirements
// files in their directories.
func linkClone() refusingFS {
	c := briefClone()
	c.MapFS["roles/requirements.yml"] = &fstest.MapFile{Data: []byte("[]\n")}
	c.MapFS["collections/requirements.yml"] = &fstest.MapFile{Data: []byte("[]\n")}
	c.MapFS["vendor/in"] = link("../vendor")     // inside
	c.MapFS["vendor/abs"] = link("/srv/other")   // absolute: refused like the clone FS does
	c.MapFS["vendor/up"] = link("../..")         // above the clone
	c.MapFS["vendor/self"] = link(".")           // the directory it is in
	c.MapFS["top"] = link(".")                   // the clone itself
	c.MapFS["vendor/loop"] = link("loop")        // never ends
	c.MapFS["vendor/git"] = link("../.git")      // git's own files
	c.MapFS["vendor/file"] = link("r.tar.gz")    // a file inside
	c.MapFS["vendor/dangling"] = link("nothing") // to nothing
	c.MapFS["vendor/empty"] = link("")           // no target
	c.MapFS["linked-roles"] = link(".")          // a requirements directory that is the clone itself
	c.MapFS["deep/a/b/c/requirements.yml"] = &fstest.MapFile{Data: []byte("[]\n")}
	c.MapFS["x:/y"] = &fstest.MapFile{Data: []byte("")} // a directory named like a URL scheme
	c.MapFS["https:/host/r.tar.gz"] = &fstest.MapFile{Data: []byte("tar")}
	c.MapFS["vendor/link/r.tar.gz"] = &fstest.MapFile{Data: []byte("tar")} // there, but refused by the clone's Stat
	return c
}

func TestCheckSource(t *testing.T) {
	cases := []struct {
		src, fileDir string
		refused      bool
		want         error // the refusal's sentinel; errOutside when empty
	}{
		// The brief's cases.
		{src: "geerlingguy.apache"},
		{src: "https://github.com/x/y.git"},
		{src: "git+https://github.com/x/y.git"},
		{src: "git+file:///srv/x"},
		{src: "git@github.com:x/y.git"},
		{src: "./vendor/r.tar.gz"},
		{src: "vendor/r.tar.gz"},
		{src: "../vendor/r.tar.gz", fileDir: "roles"},
		{src: "../vendor/c", fileDir: "collections"},
		{src: "/cache/org/repo/vendor/r.tar.gz"},
		{src: "file:///cache/org/repo/vendor/r.tar.gz"},
		{src: "./missing.tar.gz"},
		{src: "../x.tar.gz", refused: true},
		{src: "../../x.tar.gz", fileDir: "roles", refused: true},
		{src: "/etc/passwd", refused: true},
		{src: "/cache/org/other/r.tar.gz", refused: true},
		{src: "file:///etc/x.tar.gz", refused: true},
		{src: "vendor/link/r.tar.gz", refused: true},
		{src: ".git/config", refused: true},

		// Absolute paths: inside only below the clone directory, as written.
		// The clone itself is a directory source that holds links out
		// (vendor/abs): refused by the tree walk.
		{src: "/cache/org/repo", refused: true},
		{src: "/cache/org/repo/", refused: true},
		{src: "/cache/org/repo2/r.tar.gz", refused: true},
		{src: "/cache/org/repo/../other/r.tar.gz", refused: true},
		{src: "/cache/org/repo/vendor/../../x", refused: true},
		{src: "//cache/org/repo/vendor/r.tar.gz", refused: true},
		{src: "/", refused: true},

		// Paths galaxy opens as written: no ~ or variable expansion.
		{src: "~/r.tar.gz"},
		{src: "$HOME/r.tar.gz"},
		{src: " ../x.tar.gz"},
		{src: "vendor/./c/"},
		{src: "deep/../vendor/r.tar.gz"},
		{src: "../../../../vendor/r.tar.gz", fileDir: "deep/a/b/c"},
		{src: "../../../../../x", fileDir: "deep/a/b/c", refused: true},
		{src: ".GIT/config", refused: true},
		{src: "vendor/.Git", refused: true},

		// A missing directory: galaxy opens nothing below it, and a climb
		// out of the clone is still refused.
		{src: "missing/../vendor/r.tar.gz"},
		{src: "missing/../../x", refused: true},
		{src: "missing/.git/x"},

		// A file in the middle: galaxy opens nothing below it.
		{src: "vendor/r.tar.gz/x"},
		{src: "vendor/r.tar.gz/../../.."},

		// File URLs, the way urllib reads them.
		{src: "FILE:///etc/x.tar.gz", refused: true},
		{src: " file:///etc/x.tar.gz", refused: true},
		{src: "<URL:file:///etc/x.tar.gz>", refused: true},
		{src: "< URL: file:///etc/x.tar.gz >", refused: true},
		{src: " <URL:file:///etc/x.tar.gz>\t", refused: true},
		{src: "file://localhost/etc/x.tar.gz", refused: true},
		{src: "file://some.host/etc/x.tar.gz", refused: true},
		{src: "file://localhost/cache/org/repo/vendor/r.tar.gz"},
		{src: "file:///cache/org/repo/%2e%2e/other/r.tar.gz", refused: true},
		{src: "file:///cache/org/repo/vendor/r%2Etar.gz"},
		{src: "file:///cache/org/repo/vendor%2Fr.tar.gz"},
		{src: "file:///cache/org/repo/vendor/r.tar.gz?x=1#y"},
		{src: "file:///etc/x?/../../cache/org/repo/vendor/r.tar.gz", refused: true},
		{src: "file:///cache/org/repo/x#/../../../etc/passwd"},
		{src: "file:///cache/org/repo/%zz"},
		{src: "fi\tle:///etc/x.tar.gz", refused: true},
		{src: "file:///et\nc/x.tar.gz", refused: true},
		{src: "file:../x.tar.gz", refused: true},
		{src: "file:vendor/r.tar.gz"},
		{src: "file:../vendor/r.tar.gz", fileDir: "roles"},
		{src: "file:"},
		{src: "file://host"},
		{src: "files:///etc/x.tar.gz"},
		{src: "ftp://example.org/x.tar.gz"},

		// A string with :// is still a relative path to os.stat: the
		// directory x: exists in this clone.
		{src: "x://../../x.tar.gz", refused: true},
		{src: "x://y"},

		// Glob characters: galaxy expands them for a collection directory.
		{src: "vendor/*", refused: true, want: errGlob},
		{src: "vendor/c?", refused: true, want: errGlob},
		{src: "vendor/[c]", refused: true, want: errGlob},
		{src: "missing/*"},
		{src: "https://example.org/r.tar.gz?x=1"},
		{src: "https://host/r.tar.gz?x=1", refused: true, want: errGlob},
	}
	for _, tc := range cases {
		t.Run(tc.fileDir+"|"+tc.src, func(t *testing.T) {
			err := checkSource(linkClone(), testCloneDir, tc.fileDir, tc.src)
			if tc.refused {
				want := cmp.Or(tc.want, errOutside)
				if !errors.Is(err, want) {
					t.Fatalf("checkSource(%q, %q) = %v, want %v", tc.fileDir, tc.src, err, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkSource(%q, %q) = %v, want nil", tc.fileDir, tc.src, err)
			}
		})
	}
}

// TestCheckSourceWithTheBriefsClone runs the brief's cases on its fake as
// given: no roles or collections directory, no links.
func TestCheckSourceWithTheBriefsClone(t *testing.T) {
	accepted := []struct{ src, fileDir string }{
		{"geerlingguy.apache", ""}, {"https://github.com/x/y.git", ""}, {"git+https://github.com/x/y.git", ""},
		{"git+file:///srv/x", ""}, {"git@github.com:x/y.git", ""}, {"./vendor/r.tar.gz", ""},
		{"vendor/r.tar.gz", ""}, {"../vendor/r.tar.gz", "roles"}, {"../vendor/c", "collections"},
		{"/cache/org/repo/vendor/r.tar.gz", ""}, {"file:///cache/org/repo/vendor/r.tar.gz", ""}, {"/cache/org/repo", ""},
		{"./missing.tar.gz", ""},
	}
	for _, tc := range accepted {
		if err := checkSource(briefClone(), testCloneDir, tc.fileDir, tc.src); err != nil {
			t.Errorf("checkSource(%q, %q) = %v, want nil", tc.fileDir, tc.src, err)
		}
	}
	refused := []struct{ src, fileDir string }{
		{"../x.tar.gz", ""}, {"../../x.tar.gz", "roles"}, {"/etc/passwd", ""},
		{"/cache/org/other/r.tar.gz", ""}, {"file:///etc/x.tar.gz", ""},
		{"vendor/link/r.tar.gz", ""}, {".git/config", ""},
	}
	for _, tc := range refused {
		if err := checkSource(briefClone(), testCloneDir, tc.fileDir, tc.src); !errors.Is(err, errOutside) {
			t.Errorf("checkSource(%q, %q) = %v, want errOutside", tc.fileDir, tc.src, err)
		}
	}
}

// TestCheckSourceFollowsLinksLikeTheKernel: galaxy opens a source from its
// working directory, so a link is followed before a later "..", in the
// source and in the file's own directory (verified with ansible-galaxy:
// clone/vlink -> . and src vlink/../outside/r.tar.gz installed the role
// from beside the clone).
func TestCheckSourceFollowsLinksLikeTheKernel(t *testing.T) {
	cases := []struct {
		src, fileDir string
		refused      bool
		want         error // the refusal's sentinel; errOutside when empty
	}{
		{src: "vendor/in/r.tar.gz"},
		{src: "vendor/in/c/galaxy.yml"},
		{src: "vendor/file"},
		{src: "vendor/dangling"},
		{src: "vendor/self/self/r.tar.gz"},
		{src: "vendor/self/../vendor/r.tar.gz"},
		{src: "top/vendor/r.tar.gz"},
		{src: "vendor/in/../../x", refused: true},
		{src: "vendor/abs", refused: true},
		{src: "vendor/abs/x", refused: true},
		{src: "vendor/up", refused: true},
		{src: "vendor/up/other/r.tar.gz", refused: true},
		{src: "vendor/loop", refused: true},
		{src: "vendor/git", refused: true},
		{src: "vendor/empty", refused: true},
		// top -> . : lexically top/.. is the clone, to the kernel the
		// clone's parent.
		{src: "top/../other/r.tar.gz", refused: true},
		{src: "top/../x", refused: true},
		// The requirements file's own directory is a link to the clone.
		{src: "../other/r.tar.gz", fileDir: "linked-roles", refused: true},
		{src: "vendor/r.tar.gz", fileDir: "linked-roles"},
		{src: "file:../other/r.tar.gz", fileDir: "linked-roles", refused: true},
		{src: "file:///cache/org/repo/top/../other/r.tar.gz", refused: true},
		{src: "/cache/org/repo/top/../other/r.tar.gz", refused: true},
		// A glob after a link.
		{src: "vendor/in/*", refused: true, want: errGlob},
	}
	for _, tc := range cases {
		t.Run(tc.fileDir+"|"+tc.src, func(t *testing.T) {
			err := checkSource(linkClone(), testCloneDir, tc.fileDir, tc.src)
			if want := cmp.Or(tc.want, errOutside); tc.refused != errors.Is(err, want) || !tc.refused && err != nil {
				t.Fatalf("checkSource(%q, %q) = %v, want refused %v (%v)", tc.fileDir, tc.src, err, tc.refused, want)
			}
		})
	}
}

// TestCheckSourceWithoutLinkReading: a clone FS that cannot read links
// cannot tell whether ".." undoes a link, so such a path is refused.
func TestCheckSourceWithoutLinkReading(t *testing.T) {
	clone := statOnlyFS{linkClone()}
	cases := []struct {
		src, fileDir string
		refused      bool
	}{
		{src: "vendor/r.tar.gz"},
		{src: "vendor/in/r.tar.gz"},
		{src: "missing/../vendor/r.tar.gz"},
		{src: "../vendor/r.tar.gz", fileDir: "roles", refused: true},
		{src: "vendor/link/r.tar.gz", refused: true},
		{src: "/etc/passwd", refused: true},
	}
	for _, tc := range cases {
		err := checkSource(clone, testCloneDir, tc.fileDir, tc.src)
		if tc.refused != errors.Is(err, errOutside) || !tc.refused && err != nil {
			t.Errorf("checkSource(%q, %q) = %v, want refused %v", tc.fileDir, tc.src, err, tc.refused)
		}
	}
}

// TestCheckSourceOnARealClone runs the check over an os.Root, the way the
// worker opens a clone.
func TestCheckSourceOnARealClone(t *testing.T) {
	base := t.TempDir()
	cloneDir := filepath.Join(base, "org", "repo")
	for _, dir := range []string{"vendor", "roles", filepath.Join("..", "other")} {
		if err := os.MkdirAll(filepath.Join(cloneDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"vendor/r.tar.gz", "../other/r.tar.gz"} {
		if err := os.WriteFile(filepath.Join(cloneDir, f), []byte("tar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"top": ".", "linked-roles": ".", "vendor/out": "../../other", "vendor/in": "."} {
		if err := os.Symlink(target, filepath.Join(cloneDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(cloneDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	clone, ok := root.FS().(fs.StatFS)
	if !ok {
		t.Fatal("os.Root.FS is no fs.StatFS")
	}
	cases := []struct {
		src, fileDir string
		refused      bool
	}{
		{src: "../vendor/r.tar.gz", fileDir: "roles"},
		{src: "vendor/in/r.tar.gz"},
		{src: cloneDir + "/vendor/r.tar.gz"},
		{src: "file://" + cloneDir + "/vendor/r.tar.gz"},
		{src: "vendor/out/r.tar.gz", refused: true},
		{src: "top/../other/r.tar.gz", refused: true},
		{src: "../other/r.tar.gz", fileDir: "linked-roles", refused: true},
		{src: "../other/r.tar.gz", fileDir: "roles"},
		{src: "../../other/r.tar.gz", fileDir: "roles", refused: true},
	}
	for _, tc := range cases {
		err := checkSource(clone, cloneDir, tc.fileDir, tc.src)
		if tc.refused != errors.Is(err, errOutside) || !tc.refused && err != nil {
			t.Errorf("checkSource(%q, %q) = %v, want refused %v", tc.fileDir, tc.src, err, tc.refused)
		}
	}
}

func TestCheckSourcesNamesTheFileAndTheSource(t *testing.T) {
	p := plan{name: "collections/requirements.yml", sources: []string{"ns.col", "../../c"}}
	err := checkSources(t.Context(), briefClone(), testCloneDir, "collections", p)
	const want = `collections/requirements.yml: the source "../../c" is outside the repository`
	if err == nil || err.Error() != want {
		t.Fatalf("checkSources = %v, want %q", err, want)
	}
	if !errors.Is(err, errOutside) {
		t.Errorf("checkSources = %v, want errors.Is errOutside", err)
	}
}

func TestCheckSourcesAcceptsSourcesInside(t *testing.T) {
	p := plan{name: "requirements.yml", sources: []string{"ns.col", "vendor/r.tar.gz", "https://example.org/c.tar.gz"}}
	if err := checkSources(t.Context(), briefClone(), testCloneDir, "", p); err != nil {
		t.Fatalf("checkSources = %v, want nil", err)
	}
	if err := checkSources(t.Context(), briefClone(), testCloneDir, "", plan{name: "requirements.yml"}); err != nil {
		t.Fatalf("checkSources without sources = %v, want nil", err)
	}
}

func TestPyUnquote(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"abc":       "abc",
		"%41%62":    "Ab",
		"%2e%2E":    "..",
		"%zz":       "%zz",
		"%%41":      "%A",
		"%4":        "%4",
		"%":         "%",
		"a%2Fb":     "a/b",
		"%c3%bc":    "ü",
		"ü%20x":     "ü x",
		"100%25%25": "100%%",
	}
	for in, want := range cases {
		if got := pyUnquote(in); got != want {
			t.Errorf("pyUnquote(%q) = %q, want %q", in, got, want)
		}
	}
}
