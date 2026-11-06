package requirements_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

const (
	commitA = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
	commitB = "b0b1b2b3b4b5b6b7b8b9babbbcbdbebfc0c1c2c3"
)

// rolesAndCollections installs one role and one collection from one file.
const rolesAndCollections = "roles:\n  - acme.web\ncollections:\n  - acme.tools\n"

// collectionsOnly installs one collection.
const collectionsOnly = "collections:\n  - name: acme.tools\n    version: 1.0.0\n"

// rolesOnly installs one role.
const rolesOnly = "- acme.web\n"

var (
	entryPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})-[0-9a-f]{16}$`)
	tempPattern  = regexp.MustCompile(`^\.tmp-`)
)

// clone is a repository clone at <RepoCacheDir>/org/repo.
type clone struct {
	t            *testing.T
	repoCacheDir string
	dir          string
}

// newClone writes files (clone-relative slash paths) into a fresh clone.
func newClone(t *testing.T, files map[string]string) clone {
	t.Helper()
	base := t.TempDir()
	c := clone{t: t, repoCacheDir: base, dir: filepath.Join(base, "org", "repo")}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		c.write(name, content)
	}
	return c
}

func (c clone) write(name, content string) {
	c.t.Helper()
	p := filepath.Join(c.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

func (c clone) fs() fs.StatFS { return os.DirFS(c.dir).(fs.StatFS) }

func (c clone) cacheDir() string { return requirements.CacheDir(c.dir) }

// files finds the clone's requirements files below subpath.
func (c clone) files(subpath string) requirements.Files {
	c.t.Helper()
	files, err := requirements.Find(c.fs(), subpath)
	if err != nil {
		c.t.Fatalf("Find: %v", err)
	}
	return files
}

// request asks for commit's requirements of files; lines go to rec.
func (c clone) request(commit string, files requirements.Files, rec *recorder) requirements.Request {
	req := requirements.Request{
		CloneDir:     c.dir,
		Clone:        c.fs(),
		Files:        files,
		Commit:       commit,
		RepoCacheDir: c.repoCacheDir,
	}
	if rec != nil {
		req.Line = rec.add
	}
	return req
}

// ensure runs one Ensure of commit with the clone's root files.
func (c clone) ensure(svc *requirements.Service, commit string) (requirements.Entry, []string, error) {
	c.t.Helper()
	rec := &recorder{}
	entry, err := svc.Ensure(c.t.Context(), c.request(commit, c.files(""), rec))
	return entry, rec.get(), err
}

// entries lists the published entries in the cache directory.
func (c clone) entries() []string { return c.list(entryPattern) }

// temps lists the temporary directories in the cache directory.
func (c clone) temps() []string { return c.list(tempPattern) }

func (c clone) list(pattern *regexp.Regexp) []string {
	c.t.Helper()
	dirents, err := os.ReadDir(c.cacheDir())
	if err != nil && !os.IsNotExist(err) {
		c.t.Fatal(err)
	}
	var out []string
	for _, e := range dirents {
		if pattern.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// assertNoCache checks that the cache directory was never made.
func (c clone) assertNoCache() {
	c.t.Helper()
	if _, err := os.Lstat(c.cacheDir()); !os.IsNotExist(err) {
		c.t.Fatalf("the cache directory exists (%v), want none", err)
	}
}

// assertNothingLeft checks that the cache holds no entry and no temp dir.
func (c clone) assertNothingLeft() {
	c.t.Helper()
	if e, tmp := c.entries(), c.temps(); len(e) > 0 || len(tmp) > 0 {
		c.t.Fatalf("cache holds entries %v and temp dirs %v, want none", e, tmp)
	}
}

// recorder collects lines, safe for concurrent use.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

// installLine is the galaxy line the fakes send.
const installLine = "- acme.web was installed successfully"

// newGalaxy is a fake that installs one role and one collection.
func newGalaxy() *requirementstest.Galaxy {
	return &requirementstest.Galaxy{
		Roles:       map[string]string{"acme.web/tasks/main.yml": "- debug: msg=hi\n"},
		Collections: map[string]string{"ansible_collections/acme/tools/README.md": "# tools\n"},
		Lines:       []string{installLine},
	}
}

func newService(g requirements.Galaxy) *requirements.Service {
	return requirements.NewService(g, &requirementstest.Clock{}, 0)
}

// usingLine is the line of a reused entry.
func usingLine(commit string) string {
	return "Requirements: using the install for commit " + commit[:7] + " on this worker"
}

// assertErr checks that err is exactly want.
func assertErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
