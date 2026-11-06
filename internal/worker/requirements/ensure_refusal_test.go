package requirements_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// TestEnsureRefusesWhatItDidNotPutThere: a symlink or a file at the cache
// directory or at the entry is refused by name, left where it is and never
// followed (spec goal 1a).
func TestEnsureRefusesWhatItDidNotPutThere(t *testing.T) {
	plants := []struct {
		name     string
		sentinel error
		text     string
		plant    func(t *testing.T, path, target string)
	}{
		{"symlink to a directory", requirements.ErrCacheSymlink, "a symlink, which is not followed",
			func(t *testing.T, path, target string) { symlink(t, target, path) }},
		{"dangling symlink", requirements.ErrCacheSymlink, "a symlink, which is not followed",
			func(t *testing.T, path, target string) { symlink(t, filepath.Join(target, "missing"), path) }},
		{"regular file", requirements.ErrCacheNotDirectory, "not a directory",
			func(t *testing.T, path, _ string) {
				if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
	}
	places := []struct {
		name string
		// at prepares the clone and returns the path to plant at.
		at func(t *testing.T, c clone, svc *requirements.Service) string
	}{
		{"cache directory", func(_ *testing.T, c clone, _ *requirements.Service) string { return c.cacheDir() }},
		{"entry", func(t *testing.T, c clone, svc *requirements.Service) string {
			entry, _, err := c.ensure(svc, commitA)
			if err != nil {
				t.Fatalf("first Ensure: %v", err)
			}
			if err := os.RemoveAll(entry.Dir); err != nil {
				t.Fatal(err)
			}
			return entry.Dir
		}},
	}
	for _, place := range places {
		for _, p := range plants {
			t.Run(place.name+"/"+p.name, func(t *testing.T) {
				c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
				g := newGalaxy()
				svc := newService(g)
				path := place.at(t, c, svc)
				target := filepath.Join(t.TempDir(), "elsewhere")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "keep"), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
				p.plant(t, path, target)
				before, _ := os.Lstat(path)
				calls := len(g.Calls())

				_, _, err := c.ensure(svc, commitA)
				if !errors.Is(err, p.sentinel) {
					t.Fatalf("err = %v, want %v", err, p.sentinel)
				}
				if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), p.text) {
					t.Errorf("err %q does not name %s and %q", err, path, p.text)
				}
				if n := len(g.Calls()); n != calls {
					t.Errorf("galaxy ran %d more times", n-calls)
				}
				after, lerr := os.Lstat(path)
				if lerr != nil || !os.SameFile(before, after) {
					t.Errorf("the planted %s was replaced or removed (%v)", p.name, lerr)
				}
				names, err := os.ReadDir(target)
				if err != nil || len(names) != 1 {
					t.Fatalf("the link target changed: %v, %v", names, err)
				}
				if data, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(data) != "data" {
					t.Errorf("the link target's file changed: %q, %v", data, err)
				}
			})
		}
	}
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
