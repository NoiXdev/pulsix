package requirements_test

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// logBuffer collects a logger's output, safe for concurrent use.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// loggedService is a service whose warnings go to the returned buffer.
func loggedService(g requirements.Galaxy, clock requirements.Clock) (*requirements.Service, *logBuffer) {
	buf := &logBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	return requirements.NewService(g, clock, 0, requirements.WithLogger(logger)), buf
}

// assertWarned checks that one warning line names msg and path.
func assertWarned(t *testing.T, log *logBuffer, msg, path string) {
	t.Helper()
	for _, l := range strings.Split(log.String(), "\n") {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, msg) && strings.Contains(l, path) && strings.Contains(l, "err=") {
			return
		}
	}
	t.Errorf("no warning %q naming %s in the log:\n%s", msg, path, log.String())
}

// lockDir makes dir read-only (0555) after the test made it hold a file, so
// nobody but root can empty it; the test's cleanup makes the tree
// writable again.
func lockDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root removes read-only directories")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
}

// unlockTree makes every directory below dir writable again.
func unlockTree(t *testing.T, dir string) {
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
}

// assertUsable checks that the entry is returned and holds the install.
func assertUsable(t *testing.T, entry requirements.Entry, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	assertFile(t, filepath.Join(entry.RolesDir, "acme.web", "tasks", "main.yml"), "- debug: msg=hi\n")
}

func TestEnsureKeepsTheEntryWhenALeftoverCannotBeRemoved(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	unlockTree(t, c.repoCacheDir)
	lockDir(t, filepath.Join(c.cacheDir(), ".tmp-0123456789abcdef", "locked"))
	svc, log := loggedService(newGalaxy(), &requirementstest.Clock{})
	for _, run := range []string{"miss", "hit"} {
		entry, _, err := c.ensure(svc, commitA)
		assertUsable(t, entry, err)
		if run == "hit" && len(c.entries()) != 1 {
			t.Errorf("entries = %v", c.entries())
		}
	}
	assertWarned(t, log, "remove leftover requirements installs", c.cacheDir())
}

func TestEnsureKeepsTheEntryWhenTheLegacyDirCannotBeRemoved(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	unlockTree(t, c.repoCacheDir)
	lockDir(t, filepath.Join(c.dir+".galaxy", "acme.web"))
	svc, log := loggedService(newGalaxy(), &requirementstest.Clock{})
	entry, _, err := c.ensure(svc, commitA)
	assertUsable(t, entry, err)
	assertWarned(t, log, "remove the legacy galaxy directory", c.dir+".galaxy")
}

func TestEnsureKeepsTheEntryWhenTheTempEntryCannotBeRemoved(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.RemoveErr = errors.New("helper refused")
	svc, log := loggedService(g, &requirementstest.Clock{})
	entry, _, err := c.ensure(svc, commitA)
	assertUsable(t, entry, err)
	tmp := g.Calls()[0].Entry
	assertWarned(t, log, "remove the install after its take-over", tmp)
	if _, err := os.Lstat(tmp); err != nil {
		t.Fatalf("the temp entry is gone (%v), want it left for the next sweep", err)
	}

	g.RemoveErr = nil
	if _, _, err := c.ensure(svc, commitA); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if tmps := c.temps(); len(tmps) > 0 {
		t.Errorf("the next Ensure left %v", tmps)
	}
}

func TestEnsureKeepsTheEntryWhenEvictionFails(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	unlockTree(t, c.repoCacheDir)
	svc, log := loggedService(newGalaxy(), &requirementstest.Clock{Step: time.Hour})
	commit := func(n int) string { return strings.Repeat(string("0123456789"[n]), 40) }
	first, _, err := c.ensure(svc, commit(1))
	if err != nil {
		t.Fatalf("Ensure c1: %v", err)
	}
	lockDir(t, filepath.Join(first.RolesDir, "acme.web", "locked"))
	var entry requirements.Entry
	for n := 2; n <= 4; n++ {
		entry, _, err = c.ensure(svc, commit(n))
	}
	assertUsable(t, entry, err)
	assertWarned(t, log, "evict requirements entries", c.cacheDir())
}

func TestEnsureKeepsTheEntryWhenItCannotMarkItUsed(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	svc, log := loggedService(newGalaxy(), &requirementstest.Clock{})
	requirements.SetTouch(svc, func(string) error { return errors.New("read-only file system") })
	entry, _, err := c.ensure(svc, commitA)
	assertUsable(t, entry, err)
	assertWarned(t, log, "mark the requirements entry used", entry.Dir)
}
