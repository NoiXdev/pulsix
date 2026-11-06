package requirements

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestCheckSourcesRefusesOptionLikeValues: galaxy hands a role's or a
// collection's version to git checkout and git archive without "--"
// (utils/galaxy.py:79-81,94-96; collection/concrete_artifact_manager.py
// :460), so every value it may put on a command line is checked.
func TestCheckSourcesRefusesOptionLikeValues(t *testing.T) {
	cases := []struct{ name, file, want string }{
		{"a role string's version", "- https://h/x.git,--pathspec-from-file=/cache/otherorg/repo/secret.yml\n",
			`requirements.yml: the version "--pathspec-from-file=/cache/otherorg/repo/secret.yml" starts with "-"`},
		{"a role string's version after the strip", "- \"acme.web, -x\"\n",
			`requirements.yml: the version " -x" starts with "-"`},
		{"a role mapping's version", "roles:\n  - src: https://h/x.git\n    name: x\n    version: \"--pathspec-from-file=../secret.txt\"\n",
			`requirements.yml: the version "--pathspec-from-file=../secret.txt" starts with "-"`},
		{"a role mapping's numeric version", "- name: acme.web\n  version: -1\n",
			`requirements.yml: the version "-1" starts with "-"`},
		{"a role mapping's scm", "- src: https://h/x.git\n  name: x\n  scm: \"-x\"\n",
			`requirements.yml: the scm "-x" starts with "-"`},
		{"a collection mapping's version", "collections:\n  - name: https://h/x.git\n    type: git\n    version: \"--pathspec-from-file=../secret.txt\"\n",
			`requirements.yml: the version "--pathspec-from-file=../secret.txt" starts with "-"`},
		{"a collection mapping's type", "collections:\n  - name: ns.col\n    type: \" -x\"\n",
			`requirements.yml: the type " -x" starts with "-"`},
		{"a collection's source", "collections:\n  - name: ns.col\n    type: git\n    source: \"git+-x\"\n",
			`requirements.yml: the source "git+-x" starts with "-"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseFile("requirements.yml", []byte(tc.file))
			if err != nil {
				t.Fatalf("parseFile = %v", err)
			}
			err = checkSources(t.Context(), briefClone(), testCloneDir, "", p)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("checkSources = %v, want %q", err, tc.want)
			}
		})
	}
	ok := "- acme.web,1.0.0\n- src: https://h/x.git\n  name: x\n  version: v-1\n  scm: git\n" +
		"- name: acme.db\n  version: 2\n"
	p, err := parseFile("requirements.yml", []byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSources(t.Context(), briefClone(), testCloneDir, "", p); err != nil {
		t.Errorf("checkSources(accepted) = %v, want nil", err)
	}
	p, err = parseFile("requirements.yml", []byte("collections:\n  - name: ns.col\n    version: \">=1.0.0,<2.0.0\"\n    type: galaxy\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSources(t.Context(), briefClone(), testCloneDir, "", p); err != nil {
		t.Errorf("checkSources(collection range) = %v, want nil", err)
	}
}

func TestParseFileCollectsCommandValues(t *testing.T) {
	file := "roles:\n  - acme.web,1.0,web\n  - src: https://h/x.git\n    scm: git\n    version: v2\n    name: x\n    other: -ignored\n" +
		"collections:\n  - ns.col\n  - name: ns.two\n    version: 1.2.3\n    type: galaxy\n    source: https://hub.example.org\n"
	p, err := parseFile("requirements.yml", []byte(file))
	if err != nil {
		t.Fatal(err)
	}
	want := []value{
		{"version", "1.0"}, {"name", "web"},
		{"scm", "git"}, {"version", "v2"}, {"name", "x"},
		{"name", "ns.two"}, {"version", "1.2.3"}, {"type", "galaxy"},
	}
	if !reflect.DeepEqual(p.values, want) {
		t.Errorf("values = %v, want %v", p.values, want)
	}
}

// chainClone is a repository whose links each cost the check many lookups:
// every target walks x/.. 800 times before it names the next link, and
// the 40 links form one chain, so walking c resolves 820 hops.
func chainClone() fstest.MapFS {
	c := fstest.MapFS{
		"c/x": {Mode: fs.ModeDir | 0o755},
		"c/f": {Data: []byte("x")},
	}
	detour := strings.Repeat("x/../", 800)
	for i := 0; i < 40; i++ {
		next := "f"
		if i < 39 {
			next = "l" + string(rune('A'+i+1))
		}
		c["c/l"+string(rune('A'+i))] = link(detour + next)
	}
	return c
}

func TestCheckSourcesBoundsTheLookups(t *testing.T) {
	start := time.Now()
	err := checkSources(t.Context(), chainClone(), testCloneDir, "", plan{name: "r.yml", sources: []string{"c"}})
	if want := `r.yml: the source "c" needs more than 1000000 lookups to check`; err == nil || err.Error() != want {
		t.Fatalf("checkSources = %v, want %q", err, want)
	}
	t.Logf("refused after %s", time.Since(start))

	small := treeLimits{entries: maxTreeEntries, depth: maxTreeDepth, lookups: 100}
	if err := newSourceCheck(t.Context(), linkClone(), small).source(testCloneDir, "", "vendor/r.tar.gz"); err != nil {
		t.Errorf("a short path within 100 lookups: %v", err)
	}
	if err := newSourceCheck(t.Context(), chainClone(), small).source(testCloneDir, "", "c/lA"); !errors.Is(err, errTooManyLookups) {
		t.Errorf("one long link: %v, want errTooManyLookups", err)
	}
	// The budget is shared by every source of a file: c/lA costs about
	// 64 000 lookups (40 hops of 1 601 parts), so 15 fit and 16 do not.
	for n, want := range map[int]error{15: nil, 16: errTooManyLookups} {
		p := plan{name: "r.yml", sources: slices.Repeat([]string{"c/lA"}, n)}
		if err := checkSources(t.Context(), chainClone(), testCloneDir, "", p); !errors.Is(err, want) || want == nil && err != nil {
			t.Errorf("%d sources: %v, want %v", n, err, want)
		}
	}
	// The budget is shared by every source of the check.
	shared := newSourceCheck(t.Context(), briefClone(), treeLimits{entries: maxTreeEntries, depth: maxTreeDepth, lookups: 8})
	for i, want := range []bool{false, false, true} {
		err := shared.source(testCloneDir, "", "vendor/c/galaxy.yml")
		if got := errors.Is(err, errTooManyLookups); got != want {
			t.Errorf("source %d: %v, want refused %v", i+1, err, want)
		}
	}
}

// TestCheckSourceSpendsEveryLookup: vendor/c costs 5 lookups — two parts,
// the confirming Stat, the directory and its one entry — and the check
// stops at whichever of them the budget does not cover.
func TestCheckSourceSpendsEveryLookup(t *testing.T) {
	for lookups := 0; lookups <= 5; lookups++ {
		lim := treeLimits{entries: maxTreeEntries, depth: maxTreeDepth, lookups: lookups}
		err := newSourceCheck(t.Context(), briefClone(), lim).source(testCloneDir, "", "vendor/c")
		if refused := errors.Is(err, errTooManyLookups); refused != (lookups < 5) || !refused && err != nil {
			t.Errorf("%d lookups: %v, want refused %v", lookups, err, lookups < 5)
		}
	}
}

// cancellingFS cancels its context after a number of lookups.
type cancellingFS struct {
	fsys   fstest.MapFS
	after  int
	cancel context.CancelFunc
}

func (f *cancellingFS) Open(name string) (fs.File, error)          { return f.fsys.Open(name) }
func (f *cancellingFS) Stat(name string) (fs.FileInfo, error)      { return f.fsys.Stat(name) }
func (f *cancellingFS) ReadLink(name string) (string, error)       { return f.fsys.ReadLink(name) }
func (f *cancellingFS) ReadDir(name string) ([]fs.DirEntry, error) { return f.fsys.ReadDir(name) }
func (f *cancellingFS) Lstat(name string) (fs.FileInfo, error) {
	if f.after--; f.after == 0 {
		f.cancel()
	}
	return f.fsys.Lstat(name)
}

func TestCheckSourcesStopsOnTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := checkSources(ctx, briefClone(), testCloneDir, "", plan{name: "r.yml", sources: []string{"vendor/r.tar.gz"}})
	if !errors.Is(err, context.Canceled) || err.Error() != `r.yml: the source "vendor/r.tar.gz": context canceled` {
		t.Fatalf("checkSources(cancelled) = %v, want context.Canceled", err)
	}

	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	clone := &cancellingFS{fsys: chainClone(), after: 50, cancel: cancel}
	err = checkSources(ctx, clone, testCloneDir, "", plan{name: "r.yml", sources: []string{"c"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("checkSources(cancelled in the walk) = %v, want context.Canceled", err)
	}
	if clone.after < -10 {
		t.Errorf("%d lookups after the cancel, want at most 10", -clone.after)
	}
}

// linkStatFS reports a link from Stat but cannot read links.
type linkStatFS struct{ fstest.MapFS }

func (f linkStatFS) Open(name string) (fs.File, error) { return f.MapFS.Open(name) }
func (f linkStatFS) Stat(name string) (fs.FileInfo, error) {
	if name == "l" {
		return f.Lstat(name)
	}
	return f.MapFS.Stat(name)
}

func TestCheckSourceRefusesALinkItCannotRead(t *testing.T) {
	clone := statOnlyFS{linkStatFS{fstest.MapFS{"l": link("x"), "x": {Data: []byte("x")}}}}
	if err := checkSource(clone, testCloneDir, "", "l"); !errors.Is(err, errOutside) {
		t.Fatalf("checkSource = %v, want errOutside", err)
	}
}
