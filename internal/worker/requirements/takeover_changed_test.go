package requirements

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTakeOverRefusesAnEntryThatChangesWhileItIsCopied: an entry is checked
// against what was listed. Each row changes roles/r/f (or the directory
// roles/r) at the one point of the copy where only an attacker could.
func TestTakeOverRefusesAnEntryThatChangesWhileItIsCopied(t *testing.T) {
	// replace puts a new file at p. The new file exists before the old one
	// goes, so the two never share an inode number.
	replace := func(t *testing.T, p string) {
		t.Helper()
		next := p + ".next"
		if err := os.WriteFile(next, []byte("other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, p); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		path string
		// hooks returns the seams; s is the temp entry's directory.
		hooks func(t *testing.T, s string) takeOverHooks
	}{
		{"a file replaced after it was listed", "roles/r/f", func(t *testing.T, s string) takeOverHooks {
			return takeOverHooks{beforeOpen: func(p string) {
				if p == "roles/r/f" {
					replace(t, filepath.Join(s, p))
				}
			}}
		}},
		{"a file hard-linked after it was listed", "roles/r/f", func(t *testing.T, s string) takeOverHooks {
			return takeOverHooks{beforeOpen: func(p string) {
				if p != "roles/r/f" {
					return
				}
				if err := os.Link(filepath.Join(s, p), filepath.Join(s, "home", "link")); err != nil {
					t.Error(err)
				}
			}}
		}},
		{"a directory replaced after it was listed", "roles/r", func(t *testing.T, s string) takeOverHooks {
			return takeOverHooks{beforeOpen: func(p string) {
				if p != "roles/r" {
					return
				}
				if err := os.Rename(filepath.Join(s, p), filepath.Join(s, "home", "r")); err != nil {
					t.Error(err)
					return
				}
				if err := os.Mkdir(filepath.Join(s, p), 0o750); err != nil {
					t.Error(err)
				}
			}}
		}},
		{"a file that grows while it is copied", "roles/r/f", func(t *testing.T, s string) takeOverHooks {
			return takeOverHooks{afterRead: func(p string) {
				if p != "roles/r/f" {
					return
				}
				// The mtime is put back, so only the size gives it away.
				full := filepath.Join(s, p)
				fi, err := os.Stat(full)
				if err != nil {
					t.Error(err)
					return
				}
				f, err := os.OpenFile(full, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = f.Close() }()
				if _, err := f.WriteString("more\n"); err != nil {
					t.Error(err)
				}
				if err := os.Chtimes(full, fi.ModTime(), fi.ModTime()); err != nil {
					t.Error(err)
				}
			}}
		}},
		{"a file whose mtime changes while it is copied", "roles/r/f", func(t *testing.T, s string) takeOverHooks {
			return takeOverHooks{afterRead: func(p string) {
				if p != "roles/r/f" {
					return
				}
				later := time.Now().Add(time.Hour)
				if err := os.Chtimes(filepath.Join(s, p), later, later); err != nil {
					t.Error(err)
				}
			}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, root, src := takeOverSource(t)
			s := filepath.Join(dir, src)
			writeTestFile(t, filepath.Join(s, "roles/r/f"), "content\n", 0o640)
			hooks := tc.hooks(t, s)

			name, err := takeOverWith(t.Context(), root, src, defaultTakeOverLimits(), hooks)
			want := "take over the install: " + tc.path + ": changed while it was copied"
			if err == nil || err.Error() != want || !errors.Is(err, errTakeOverChanged) {
				t.Fatalf("takeOverWith = %q, %v; want %q wrapping errTakeOverChanged", name, err, want)
			}
			onlyNames(t, dir, src)
		})
	}
}

// TestTakeOverNamesADestinationError: the worker's own copy is also checked.
// An entry that already exists in the destination (a second writer, or a
// crafted name that collides) refuses the take-over naming the path, with
// the cause kept for errors.Is.
func TestTakeOverNamesADestinationError(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		build func(t *testing.T, s string)
		// make creates p in the destination d.
		make func(t *testing.T, d string)
	}{
		{"a file", "roles/r/f", func(t *testing.T, s string) {
			writeTestFile(t, filepath.Join(s, "roles/r/f"), "content\n", 0o640)
		}, func(t *testing.T, d string) {
			writeTestFile(t, filepath.Join(d, "roles/r/f"), "squatter\n", 0o600)
		}},
		{"a directory", "roles/r/d", func(t *testing.T, s string) {
			if err := os.MkdirAll(filepath.Join(s, "roles/r/d"), 0o750); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, d string) {
			if err := os.MkdirAll(filepath.Join(d, "roles/r/d"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"a symlink", "roles/r/l", func(t *testing.T, s string) {
			writeTestFile(t, filepath.Join(s, "roles/r/f"), "content\n", 0o640)
			symlinkTest(t, "f", filepath.Join(s, "roles/r/l"))
		}, func(t *testing.T, d string) {
			symlinkTest(t, "elsewhere", filepath.Join(d, "roles/r/l"))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, root, src := takeOverSource(t)
			tc.build(t, filepath.Join(dir, src))
			created := false
			hooks := takeOverHooks{beforeOpen: func(p string) {
				if p != tc.path {
					return
				}
				tc.make(t, destinationEntry(t, dir, src))
				created = true
			}}

			name, err := takeOverWith(t.Context(), root, src, defaultTakeOverLimits(), hooks)
			if !created {
				t.Fatalf("the hook never saw %s", tc.path)
			}
			prefix := "take over the install: " + tc.path + ": "
			if err == nil || !strings.HasPrefix(err.Error(), prefix) || !errors.Is(err, fs.ErrExist) {
				t.Fatalf("takeOverWith = %q, %v; want %q... wrapping fs.ErrExist", name, err, prefix)
			}
			onlyNames(t, dir, src)
		})
	}
}

// destinationEntry is the cache's temp entry that is not src: the one a
// take-over copies into.
func destinationEntry(t *testing.T, dir, src string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) && e.Name() != src {
			found = append(found, e.Name())
		}
	}
	if len(found) != 1 {
		t.Fatalf("destination entries = %v, want exactly one", found)
	}
	return filepath.Join(dir, found[0])
}
