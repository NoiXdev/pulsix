package requirements_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

func TestEnsureNothingCountedTouchesNothing(t *testing.T) {
	c := newClone(t, nil)
	g := newGalaxy()
	rec := &recorder{}
	entry, err := newService(g).Ensure(t.Context(), c.request(commitA, requirements.Files{}, rec))
	if err != nil || !entry.IsZero() {
		t.Fatalf("Ensure = %+v, %v, want a zero entry", entry, err)
	}
	if lines := rec.get(); len(lines) > 0 {
		t.Errorf("lines = %q, want none", lines)
	}
	if len(g.Calls()) > 0 {
		t.Errorf("galaxy was called: %+v", g.Calls())
	}
	c.assertNoCache()
}

func TestEnsureListsNothing(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"one file", map[string]string{"requirements.yml": "# none\n"},
			"Requirements: requirements.yml lists nothing to install"},
		{"two files", map[string]string{"requirements.yml": "# none\n", "roles/requirements.yml": "[]\n"},
			"Requirements: requirements.yml, roles/requirements.yml list nothing to install"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClone(t, tc.files)
			g := newGalaxy()
			entry, lines, err := c.ensure(newService(g), commitA)
			if err != nil || !entry.IsZero() {
				t.Fatalf("Ensure = %+v, %v, want a zero entry", entry, err)
			}
			if !slices.Equal(lines, []string{tc.want}) {
				t.Errorf("lines = %q, want [%q]", lines, tc.want)
			}
			if len(g.Calls()) > 0 {
				t.Errorf("galaxy was called: %+v", g.Calls())
			}
			c.assertNoCache()
		})
	}
}

func TestEnsureInstallsAndPublishes(t *testing.T) {
	c := newClone(t, map[string]string{
		"requirements.yml":             rolesAndCollections,
		"collections/requirements.yml": collectionsOnly,
	})
	g := newGalaxy()
	var cfgs, inputs []string
	g.OnInstall = func(in requirements.Install) error {
		cfg, err := os.ReadFile(filepath.Join(in.Entry, "input", "galaxy.cfg"))
		cfgs = append(cfgs, string(cfg)+errText(err))
		data, err := os.ReadFile(in.File)
		inputs = append(inputs, string(data)+errText(err))
		return nil
	}
	entry, lines, err := c.ensure(newService(g), commitA)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	name := filepath.Base(entry.Dir)
	if filepath.Dir(entry.Dir) != c.cacheDir() || !entryPattern.MatchString(name) || !strings.HasPrefix(name, commitA+"-") {
		t.Fatalf("Entry.Dir = %q, want %s/%s-<16 hex>", entry.Dir, c.cacheDir(), commitA)
	}
	if entry.RolesDir != filepath.Join(entry.Dir, "roles") || entry.CollectionsDir != filepath.Join(entry.Dir, "collections") {
		t.Errorf("Entry = %+v", entry)
	}
	assertFile(t, filepath.Join(entry.RolesDir, "acme.web", "tasks", "main.yml"), "- debug: msg=hi\n")
	assertFile(t, filepath.Join(entry.CollectionsDir, "ansible_collections", "acme", "tools", "README.md"), "# tools\n")

	calls := g.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want 3", calls)
	}
	tmp := calls[0].Entry
	if filepath.Dir(tmp) != c.cacheDir() || !tempPattern.MatchString(filepath.Base(tmp)) {
		t.Fatalf("Install.Entry = %q, want a temp dir in %s", tmp, c.cacheDir())
	}
	want := []requirements.Install{
		{Kind: requirements.KindRole, File: filepath.Join(tmp, "input", "1.yml"), Dir: c.dir},
		{Kind: requirements.KindCollection, File: filepath.Join(tmp, "input", "1.yml"), Dir: c.dir},
		{Kind: requirements.KindCollection, File: filepath.Join(tmp, "input", "2.yml"), Dir: filepath.Join(c.dir, "collections")},
	}
	for i := range want {
		want[i].Entry, want[i].CloneDir, want[i].RepoCacheDir = tmp, c.dir, c.repoCacheDir
	}
	if !slices.Equal(calls, want) {
		t.Errorf("calls =\n%+v\nwant\n%+v", calls, want)
	}
	if wantIn := []string{rolesAndCollections, rolesAndCollections, collectionsOnly}; !slices.Equal(inputs, wantIn) {
		t.Errorf("galaxy read %q, want %q", inputs, wantIn)
	}
	if wantCfg := slices.Repeat([]string{"[defaults]\n"}, 3); !slices.Equal(cfgs, wantCfg) {
		t.Errorf("galaxy.cfg = %q, want %q", cfgs, wantCfg)
	}
	if !slices.Contains(g.Removed(), tmp) {
		t.Errorf("Removed() = %q, want the temp entry %q", g.Removed(), tmp)
	}

	if len(lines) != 5 {
		t.Fatalf("lines = %q, want 5", lines)
	}
	if want := "Requirements: installing for commit " + commitA[:7] + " from requirements.yml, collections/requirements.yml"; lines[0] != want {
		t.Errorf("first line = %q, want %q", lines[0], want)
	}
	if !slices.Equal(lines[1:4], slices.Repeat([]string{installLine}, 3)) {
		t.Errorf("galaxy lines = %q", lines[1:4])
	}
	if !regexp.MustCompile(`^Requirements: installed in \d+\.\d s$`).MatchString(lines[4]) {
		t.Errorf("last line = %q", lines[4])
	}
	if tmps := c.temps(); len(tmps) > 0 {
		t.Errorf("temp dirs left: %v", tmps)
	}
	assertMode(t, c.cacheDir(), os.ModeDir|0o700)
}

func TestEnsureReusesTheEntryOfItsCommit(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesAndCollections})
	g := newGalaxy()
	svc := newService(g)
	first, _, err := c.ensure(svc, commitA)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	calls := len(g.Calls())
	again, lines, err := c.ensure(svc, commitA)
	if err != nil || again != first {
		t.Fatalf("second Ensure = %+v, %v, want %+v", again, err, first)
	}
	if len(g.Calls()) != calls {
		t.Errorf("the second Ensure called galaxy: %+v", g.Calls()[calls:])
	}
	if want := []string{usingLine(commitA)}; !slices.Equal(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}

func TestEnsureKeysTheEntryByCommitAndContent(t *testing.T) {
	c := newClone(t, map[string]string{
		"requirements.yml":         rolesOnly,
		"ansible/requirements.yml": rolesOnly,
	})
	g := newGalaxy()
	svc := newService(g)
	var dirs []string
	for i, run := range []struct {
		commit  string
		subpath string
	}{{commitA, ""}, {commitB, ""}, {commitA, "ansible"}} {
		entry, err := svc.Ensure(t.Context(), c.request(run.commit, c.files(run.subpath), nil))
		if err != nil {
			t.Fatalf("Ensure %d: %v", i, err)
		}
		if slices.Contains(dirs, entry.Dir) {
			t.Fatalf("Ensure %d reused %s", i, entry.Dir)
		}
		if len(g.Calls()) != i+1 {
			t.Fatalf("after Ensure %d galaxy ran %d times, want %d", i, len(g.Calls()), i+1)
		}
		dirs = append(dirs, entry.Dir)
	}
	if got := c.entries(); len(got) != 3 {
		t.Errorf("entries = %v, want 3", got)
	}
}

func TestEnsureRemovesAStaleTempDir(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	stale := filepath.Join(c.cacheDir(), ".tmp-0123456789abcdef")
	plant := func() {
		if err := os.MkdirAll(filepath.Join(stale, "roles", "x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	svc := newService(newGalaxy())
	for _, run := range []string{"miss", "hit"} {
		plant()
		if _, _, err := c.ensure(svc, commitA); err != nil {
			t.Fatalf("Ensure (%s): %v", run, err)
		}
		if _, err := os.Lstat(stale); !os.IsNotExist(err) {
			t.Errorf("after a %s the stale temp dir is still there (%v)", run, err)
		}
	}
}

func TestEnsureRemovesTheLegacyGalaxyDir(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		gone    bool
	}{{"lists nothing", "# none\n", false}, {"installs", rolesOnly, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClone(t, map[string]string{"requirements.yml": tc.content})
			legacy := c.dir + ".galaxy"
			if err := os.MkdirAll(filepath.Join(legacy, "acme.web", "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.ensure(newService(newGalaxy()), commitA); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			_, err := os.Lstat(legacy)
			if gone := os.IsNotExist(err); gone != tc.gone {
				t.Errorf("legacy dir gone = %v (%v), want %v", gone, err, tc.gone)
			}
		})
	}
}

func TestEnsureEvictsTheLeastRecentlyUsed(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	svc := requirements.NewService(newGalaxy(), &requirementstest.Clock{Step: time.Hour}, 0)
	commit := func(n int) string { return strings.Repeat(string("0123456789"[n]), 40) }
	ensure := func(n int) {
		t.Helper()
		if _, _, err := c.ensure(svc, commit(n)); err != nil {
			t.Fatalf("Ensure c%d: %v", n, err)
		}
	}
	kept := func(want ...int) {
		t.Helper()
		var got []string
		for _, e := range c.entries() {
			got = append(got, e[:40])
		}
		var wantCommits []string
		for _, n := range want {
			wantCommits = append(wantCommits, commit(n))
		}
		slices.Sort(got)
		if !slices.Equal(got, wantCommits) {
			t.Fatalf("entries of %v, want of %v", got, wantCommits)
		}
	}
	for n := 1; n <= 4; n++ {
		ensure(n)
	}
	kept(2, 3, 4)
	ensure(2)
	ensure(5)
	kept(2, 4, 5)
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v, want %q", path, data, err, want)
	}
	assertMode(t, path, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != want {
		t.Errorf("%s mode = %v, want %v", path, fi.Mode(), want)
	}
}

func errText(err error) string {
	if err != nil {
		return " (" + err.Error() + ")"
	}
	return ""
}
