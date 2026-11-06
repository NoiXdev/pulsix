package requirements

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// treeFS is a clone whose col/unreadable cannot be listed.
type treeFS struct{ refusingFS }

func (f treeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "col/unreadable" {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("boom")}
	}
	return f.MapFS.ReadDir(name)
}

// treeClone holds directory sources (collections of type dir and subdirs)
// with links inside them.
func treeClone() treeFS {
	file := func() *fstest.MapFile { return &fstest.MapFile{Data: []byte("x")} }
	return treeFS{refusingFS{fstest.MapFS{
		"ansible/collections/requirements.yml": file(),
		"shared/f.txt":                         file(),
		"col/ok/galaxy.yml":                    file(),
		"col/ok/plugins/x.py":                  file(),
		"col/ok/plugins/alias.py":              link("x.py"),         // a file inside the collection
		"col/ok/self":                          link("."),            // the collection itself
		"col/ok/shared":                        link("../../shared"), // a directory elsewhere in the clone
		"col/ok/dangling":                      link("nothing"),
		"col/ok/.git/out":                      link("/etc"), // .git is not read
		"col/bad/galaxy.yml":                   file(),
		"col/bad/leak.txt":                     link("../../../outside/secret.txt"),
		"col/absleak/galaxy.yml":               file(),
		"col/absleak/sub/passwd":               link("/etc/passwd"),
		"col/deep/a/b/c/out":                   link("../../../../../../x"),
		"col/viabad/l":                         link("../bad"),
		"col/gitlink/l":                        link("../../.git/config"),
		"col/loop/a":                           link("b"),
		"col/loop/b":                           link("a"),
		"col/unreadable/x":                     file(),
		"ns/good/galaxy.yml":                   file(),
		"ns/evil":                              link("../../other/coll"),
		"ns2/good/galaxy.yml":                  file(),
		"ns2/inner":                            link("../col/ok"),
	}}}
}

// TestCheckSourceWalksDirectorySources: galaxy reads a directory source's
// files, following a file link out of it (a dir collection) and a child
// directory that links out (a subdirs collection; verified with
// ansible-galaxy), so every link in the tree must stay in the clone.
func TestCheckSourceWalksDirectorySources(t *testing.T) {
	cases := []struct {
		src, fileDir string
		link         string // the refused link, clone-relative; "" when accepted
	}{
		{src: "col/ok"},
		{src: "../../col/ok", fileDir: "ansible/collections"},
		{src: "file:///cache/org/repo/col/bad"}, // urllib cannot open a directory; the path form is checked
		{src: "col/bad/galaxy.yml"},
		{src: "ns2"},
		{src: "col/bad", link: "col/bad/leak.txt"},
		{src: "col/bad/", link: "col/bad/leak.txt"},
		{src: "/cache/org/repo/col/bad", link: "col/bad/leak.txt"},
		{src: "col/absleak", link: "col/absleak/sub/passwd"},
		{src: "col/deep", link: "col/deep/a/b/c/out"},
		{src: "col/viabad", link: "col/bad/leak.txt"},
		{src: "col/gitlink", link: "col/gitlink/l"},
		{src: "col/loop", link: "col/loop/a"},
		{src: "ns", link: "ns/evil"},
	}
	for _, tc := range cases {
		t.Run(tc.fileDir+"|"+tc.src, func(t *testing.T) {
			err := checkSource(treeClone(), testCloneDir, tc.fileDir, tc.src)
			if tc.link == "" {
				if err != nil {
					t.Fatalf("checkSource(%q, %q) = %v, want nil", tc.fileDir, tc.src, err)
				}
				return
			}
			var le *linkOutsideError
			if !errors.As(err, &le) || !errors.Is(err, errOutside) {
				t.Fatalf("checkSource(%q, %q) = %v, want a linkOutsideError", tc.fileDir, tc.src, err)
			}
			if le.link != tc.link || !strings.Contains(err.Error(), `the link "`+tc.link+`" leads outside the repository`) {
				t.Errorf("checkSource(%q, %q) = %v, want the link %q", tc.fileDir, tc.src, err, tc.link)
			}
		})
	}
}

func TestCheckSourceReportsAnUnreadableDirectory(t *testing.T) {
	err := checkSource(treeClone(), testCloneDir, "", "col/unreadable")
	if err == nil || errors.Is(err, errOutside) || !strings.Contains(err.Error(), "read col/unreadable: ") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("checkSource = %v, want the read error", err)
	}
}

func TestCheckSourceBoundsTheWalk(t *testing.T) {
	if err := newSourceCheck(t.Context(), treeClone(), treeLimits{entries: 3, depth: 64, lookups: maxLookups}).source(testCloneDir, "", "col/ok"); !errors.Is(err, errTooManyEntries) {
		t.Errorf("entries: %v, want errTooManyEntries", err)
	}
	if err := newSourceCheck(t.Context(), treeClone(), treeLimits{entries: 100, depth: 0, lookups: maxLookups}).source(testCloneDir, "", "col/ok"); !errors.Is(err, errTooDeep) {
		t.Errorf("depth: %v, want errTooDeep", err)
	}
	if err := newSourceCheck(t.Context(), treeClone(), treeLimits{entries: 100, depth: 3, lookups: maxLookups}).source(testCloneDir, "", "col/ok"); err != nil {
		t.Errorf("within bounds: %v, want nil", err)
	}
	// The default depth.
	deep := fstest.MapFS{strings.Repeat("d/", maxTreeDepth+2) + "f": {Data: []byte("x")}}
	if err := checkSource(deep, testCloneDir, "", "d"); !errors.Is(err, errTooDeep) {
		t.Errorf("default depth: %v, want errTooDeep", err)
	}
	ok := fstest.MapFS{strings.Repeat("d/", maxTreeDepth+1) + "f": {Data: []byte("x")}}
	if err := checkSource(ok, testCloneDir, "", "d"); err != nil {
		t.Errorf("default depth, at the limit: %v, want nil", err)
	}
}

// TestCheckSourceWalksARealClone mirrors the galaxy probes on an os.Root:
// a subdirs child linked out and a dir collection holding a link to an
// outside file are refused, a dir collection with an internal link is not.
func TestCheckSourceWalksARealClone(t *testing.T) {
	base := t.TempDir()
	cloneDir := filepath.Join(base, "org", "repo")
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ln := func(target, p string) {
		t.Helper()
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(base, "outside", "secret.txt"))
	mk(filepath.Join(base, "outside", "cdir", "galaxy.yml"))
	mk(filepath.Join(cloneDir, "vendor", "ns", "good", "galaxy.yml"))
	ln(filepath.Join(base, "outside", "cdir"), filepath.Join(cloneDir, "vendor", "ns", "evil"))
	mk(filepath.Join(cloneDir, "vendor", "cdir2", "galaxy.yml"))
	ln(filepath.Join(base, "outside", "secret.txt"), filepath.Join(cloneDir, "vendor", "cdir2", "leak.txt"))
	mk(filepath.Join(cloneDir, "vendor", "cdir3", "plugins", "x.py"))
	ln("plugins/x.py", filepath.Join(cloneDir, "vendor", "cdir3", "alias.py"))
	root, err := os.OpenRoot(cloneDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	clone := root.FS().(fs.StatFS)
	for src, wantLink := range map[string]string{
		"vendor/ns":    "vendor/ns/evil",
		"vendor/cdir2": "vendor/cdir2/leak.txt",
		"vendor/cdir3": "",
	} {
		err := checkSource(clone, cloneDir, "", src)
		var le *linkOutsideError
		switch {
		case wantLink == "" && err != nil:
			t.Errorf("checkSource(%q) = %v, want nil", src, err)
		case wantLink != "" && (!errors.As(err, &le) || le.link != wantLink):
			t.Errorf("checkSource(%q) = %v, want the link %q", src, err, wantLink)
		}
	}
}

func TestCheckSourcesMessages(t *testing.T) {
	cases := []struct {
		name, fileDir, src, want string
	}{
		{"collections/requirements.yml", "ansible/collections", "../../col/bad",
			`collections/requirements.yml: the source "../../col/bad" holds the link "../col/bad/leak.txt", which leads outside the repository`},
		{"requirements.yml", "", "col/bad",
			`requirements.yml: the source "col/bad" holds the link "col/bad/leak.txt", which leads outside the repository`},
		{"requirements.yml", "", "col/unreadable",
			`requirements.yml: the source "col/unreadable": read col/unreadable: readdir col/unreadable: boom`},
		{"requirements.yml", "", "-x", `requirements.yml: the source "-x" starts with "-"`},
		{"requirements.yml", "", " -x", `requirements.yml: the source " -x" starts with "-"`},
		{"requirements.yml", "", "--upload-pack=touch x", `requirements.yml: the source "--upload-pack=touch x" starts with "-"`},
		{"requirements.yml", "", "git+--upload-pack=x", `requirements.yml: the source "git+--upload-pack=x" starts with "-"`},
		{"requirements.yml", "", "x+ -y", `requirements.yml: the source "x+ -y" starts with "-"`},
		{"requirements.yml", "", "https://h/x.git,-b", `requirements.yml: the source "https://h/x.git,-b" starts with "-"`},
		{"requirements.yml", "", "https://h/x.git, -b", `requirements.yml: the source "https://h/x.git, -b" starts with "-"`},
	}
	for _, tc := range cases {
		err := checkSources(t.Context(), treeClone(), testCloneDir, tc.fileDir, plan{name: tc.name, sources: []string{"ns.col", tc.src}})
		if err == nil || err.Error() != tc.want {
			t.Errorf("checkSources(t.Context(), %q) = %v, want %q", tc.src, err, tc.want)
		}
		if strings.Contains(tc.want, "leads outside") && !errors.Is(err, errOutside) {
			t.Errorf("checkSources(t.Context(), %q) = %v, want errors.Is errOutside", tc.src, err)
		}
	}
	for _, src := range []string{"a-b", "ns.col", "https://h/-x", "git+https://h/x.git,v1", "x.tar.gz-1"} {
		if err := checkSources(t.Context(), treeClone(), testCloneDir, "", plan{name: "r.yml", sources: []string{src}}); err != nil {
			t.Errorf("checkSources(t.Context(), %q) = %v, want nil", src, err)
		}
	}
	deep := fstest.MapFS{strings.Repeat("d/", maxTreeDepth+2) + "f": {Data: []byte("x")}}
	err := checkSources(t.Context(), deep, testCloneDir, "", plan{name: "r.yml", sources: []string{"d"}})
	if want := `r.yml: the source "d" is nested deeper than 64 directories`; err == nil || err.Error() != want {
		t.Errorf("checkSources(t.Context(), deep) = %v, want %q", err, want)
	}
	err = sourceError("r.yml", "", "s", errTooManyEntries)
	if want := `r.yml: the source "s" has more than 50000 files and directories to check`; err.Error() != want {
		t.Errorf("sourceError = %v, want %q", err, want)
	}
}

func TestRelTo(t *testing.T) {
	cases := []struct{ root, p, want string }{
		{"", "col/x", "col/x"},
		{"ansible", "ansible/col/x", "col/x"},
		{"ansible", "ansible", "."},
		{"ansible", "col/x", "../col/x"},
		{"a/b", "a/c/x", "../c/x"},
		{"a/b", "x", "../../x"},
	}
	for _, tc := range cases {
		if got := relTo(tc.root, tc.p); got != tc.want {
			t.Errorf("relTo(%q, %q) = %q, want %q", tc.root, tc.p, got, tc.want)
		}
	}
}

func TestRequirementsRoot(t *testing.T) {
	cases := []struct{ fileDir, name, want string }{
		{"", "requirements.yml", ""},
		{"ansible", "requirements.yml", "ansible"},
		{"ansible/collections", "collections/requirements.yml", "ansible"},
		{"collections", "collections/requirements.yml", ""},
	}
	for _, tc := range cases {
		if got := requirementsRoot(tc.fileDir, tc.name); got != tc.want {
			t.Errorf("requirementsRoot(%q, %q) = %q, want %q", tc.fileDir, tc.name, got, tc.want)
		}
	}
}
