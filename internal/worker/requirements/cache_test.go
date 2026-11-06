package requirements

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCacheDir(t *testing.T) {
	cases := map[string]string{
		"/c/o/r":  "/c/o/r.requirements",
		"/c/o/r/": "/c/o/r.requirements",
	}
	for clone, want := range cases {
		if got := CacheDir(clone); got != want {
			t.Errorf("CacheDir(%q) = %q, want %q", clone, got, want)
		}
	}
}

func TestMkTemp(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)
	pattern := regexp.MustCompile(`^\.tmp-[0-9a-f]{16}$`)

	a, err := mkTemp(root)
	if err != nil {
		t.Fatalf("mkTemp: %v", err)
	}
	b, err := mkTemp(root)
	if err != nil {
		t.Fatalf("mkTemp: %v", err)
	}
	if a == b {
		t.Fatalf("two calls gave the same name %q", a)
	}
	for _, name := range []string{a, b} {
		if !pattern.MatchString(name) {
			t.Errorf("name %q does not match %s", name, pattern)
		}
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want a directory with 0700", name, fi.Mode())
		}
	}
}

func TestMkTempRetriesATakenName(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)
	taken := bytes.Repeat([]byte{0xab}, 8)
	fresh := bytes.Repeat([]byte{0xcd}, 8)
	if err := os.Mkdir(filepath.Join(dir, ".tmp-abababababababab"), 0o700); err != nil {
		t.Fatal(err)
	}

	name, err := mkTempWith(root, bytes.NewReader(append(slices.Clone(taken), fresh...)))
	if err != nil {
		t.Fatalf("mkTempWith: %v", err)
	}
	if name != ".tmp-cdcdcdcdcdcdcdcd" {
		t.Fatalf("name = %q, want the second draw", name)
	}

	always := bytes.Repeat(taken, 100)
	if _, err := mkTempWith(root, bytes.NewReader(always)); err == nil {
		t.Fatal("mkTempWith with only taken names succeeded, want an error")
	}
	if _, err := mkTempWith(root, bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("mkTempWith without randomness: err = %v, want io.EOF", err)
	}
}

// mkdirs creates each directory below dir.
func mkdirs(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// writeFiles creates each file below dir with its name as content.
func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// names lists dir.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// outsideDir makes a directory with one file next to the cache and returns its path.
func outsideDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "outside")
	mkdirs(t, dir, ".")
	writeFiles(t, dir, "keep")
	return dir
}

func assertOutsideIntact(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "keep"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("outside dir changed: %q, %v", data, err)
	}
}

func entryKey(n byte) string {
	return strings.Repeat(string("0123456789abcdef"[n%16]), 40) + "-" + testHash
}

func TestSweepTemp(t *testing.T) {
	t.Run("removes the temp dirs and nothing else", func(t *testing.T) {
		dir := t.TempDir()
		outside := outsideDir(t)
		mkdirs(t, dir, ".tmp-dead/roles/x", entryKey(1)+"/roles")
		writeFiles(t, dir, ".lock", ".tmp-file")
		if err := os.Symlink(outside, filepath.Join(dir, ".tmp-link")); err != nil {
			t.Fatal(err)
		}

		err := sweepTemp(openTestRoot(t, dir), func(name string) error {
			t.Errorf("fallback called with %q", name)
			return nil
		})
		if err != nil {
			t.Fatalf("sweepTemp: %v", err)
		}
		want := []string{".lock", entryKey(1)}
		if got := names(t, dir); !slices.Equal(got, want) {
			t.Fatalf("left %v, want %v", got, want)
		}
		assertOutsideIntact(t, outside)
	})

	stuck := func(t *testing.T) string {
		t.Helper()
		if os.Geteuid() == 0 {
			t.Skip("root removes a read-only directory's contents")
		}
		dir := t.TempDir()
		mkdirs(t, dir, ".tmp-stuck/roles")
		writeFiles(t, dir, ".tmp-stuck/roles/file")
		roles := filepath.Join(dir, ".tmp-stuck", "roles")
		if err := os.Chmod(roles, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(roles, 0o700) })
		return dir
	}

	t.Run("hands what it cannot remove to the fallback", func(t *testing.T) {
		dir := stuck(t)
		var called []string
		err := sweepTemp(openTestRoot(t, dir), func(name string) error {
			called = append(called, name)
			if err := os.Chmod(filepath.Join(dir, name, "roles"), 0o700); err != nil {
				return err
			}
			return os.RemoveAll(filepath.Join(dir, name))
		})
		if err != nil {
			t.Fatalf("sweepTemp: %v", err)
		}
		if !slices.Equal(called, []string{".tmp-stuck"}) {
			t.Fatalf("fallback called with %v, want [.tmp-stuck]", called)
		}
		if got := names(t, dir); len(got) != 0 {
			t.Fatalf("left %v, want nothing", got)
		}
	})

	t.Run("names the temp dir when the fallback fails", func(t *testing.T) {
		dir := stuck(t)
		mkdirs(t, dir, ".tmp-other")
		err := sweepTemp(openTestRoot(t, dir), func(string) error { return errors.New("helper refused") })
		if err == nil || !strings.Contains(err.Error(), ".tmp-stuck") || !strings.Contains(err.Error(), "helper refused") {
			t.Fatalf("err = %v, want it to name .tmp-stuck and the fallback's error", err)
		}
		if got := names(t, dir); !slices.Equal(got, []string{".tmp-stuck"}) {
			t.Fatalf("left %v, want only .tmp-stuck (the others still swept)", got)
		}
	})

	t.Run("names the temp dir when it stays after the fallback", func(t *testing.T) {
		dir := stuck(t)
		err := sweepTemp(openTestRoot(t, dir), func(string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), ".tmp-stuck") {
			t.Fatalf("err = %v, want it to name .tmp-stuck", err)
		}
	})

	t.Run("without a fallback", func(t *testing.T) {
		dir := stuck(t)
		err := sweepTemp(openTestRoot(t, dir), nil)
		if err == nil || !strings.Contains(err.Error(), ".tmp-stuck") {
			t.Fatalf("err = %v, want it to name .tmp-stuck", err)
		}
	})
}

func TestEvictKeepsTheThreeMostRecentlyUsed(t *testing.T) {
	if maxEntries != 3 {
		t.Fatalf("maxEntries = %d, want 3", maxEntries)
	}
	dir := t.TempDir()
	outside := outsideDir(t)
	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Minute) }

	// Entries 1..5 with mtimes t1..t5, created in a shuffled order so the
	// directory order says nothing.
	for _, i := range []int{3, 1, 5, 2, 4} {
		mkdirs(t, dir, entryKey(byte(i))+"/roles/r")
		if err := os.Chtimes(filepath.Join(dir, entryKey(byte(i))), at(i), at(i)); err != nil {
			t.Fatal(err)
		}
	}
	mkdirs(t, dir, ".tmp-x")
	writeFiles(t, dir, ".lock", "notes", entryKey(7))
	link := filepath.Join(dir, entryKey(6))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	// Chtimes follows the link: the target reads as the oldest entry.
	if err := os.Chtimes(link, at(0), at(0)); err != nil {
		t.Fatal(err)
	}

	if err := evict(openTestRoot(t, dir), maxEntries); err != nil {
		t.Fatalf("evict: %v", err)
	}

	want := []string{".lock", ".tmp-x", entryKey(3), entryKey(4), entryKey(5), entryKey(6), entryKey(7), "notes"}
	slices.Sort(want)
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Fatalf("left %v, want %v", got, want)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the entry-named symlink is gone or changed: %v, %v", fi, err)
	}
	assertOutsideIntact(t, outside)
}

func TestEvictBreaksTiesByName(t *testing.T) {
	dir := t.TempDir()
	same := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, i := range []byte{2, 1, 3} {
		mkdirs(t, dir, entryKey(i))
		if err := os.Chtimes(filepath.Join(dir, entryKey(i)), same, same); err != nil {
			t.Fatal(err)
		}
	}
	root := openTestRoot(t, dir)
	if err := evict(root, 5); err != nil {
		t.Fatalf("evict above the count: %v", err)
	}
	if got := len(names(t, dir)); got != 3 {
		t.Fatalf("evict above the count left %d entries, want 3", got)
	}
	if err := evict(root, 1); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{entryKey(1)}) {
		t.Fatalf("left %v, want the first name of the tie", got)
	}
}

func TestEvictNamesWhatItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a read-only directory's contents")
	}
	dir := t.TempDir()
	old, young := entryKey(1), entryKey(2)
	mkdirs(t, dir, old+"/roles", young)
	writeFiles(t, dir, old+"/roles/file")
	roles := filepath.Join(dir, old, "roles")
	if err := os.Chmod(roles, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roles, 0o700) })
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, old), t0, t0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, young), t0.Add(time.Minute), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	err := evict(openTestRoot(t, dir), 1)
	if err == nil || !strings.Contains(err.Error(), old) {
		t.Fatalf("err = %v, want it to name %s", err, old)
	}
}

func TestRemoveLegacyGalaxyDir(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		org := t.TempDir()
		clone := filepath.Join(org, "repo")
		mkdirs(t, org, "repo", "repo.galaxy/acme.web/x", "repo.requirements")
		if err := removeLegacy(clone); err != nil {
			t.Fatalf("removeLegacy: %v", err)
		}
		if got := names(t, org); !slices.Equal(got, []string{"repo", "repo.requirements"}) {
			t.Fatalf("left %v, want the clone and its cache", got)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		org := t.TempDir()
		outside := outsideDir(t)
		clone := filepath.Join(org, "repo")
		mkdirs(t, org, "repo")
		if err := os.Symlink(outside, clone+".galaxy"); err != nil {
			t.Fatal(err)
		}
		if err := removeLegacy(clone); err != nil {
			t.Fatalf("removeLegacy: %v", err)
		}
		if _, err := os.Lstat(clone + ".galaxy"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the link is still there: %v", err)
		}
		assertOutsideIntact(t, outside)
	})

	t.Run("absent", func(t *testing.T) {
		org := t.TempDir()
		mkdirs(t, org, "repo")
		if err := removeLegacy(filepath.Join(org, "repo")); err != nil {
			t.Fatalf("removeLegacy: %v", err)
		}
	})

	t.Run("parent missing", func(t *testing.T) {
		err := removeLegacy(filepath.Join(t.TempDir(), "gone", "repo"))
		if err == nil || !strings.Contains(err.Error(), "repo.galaxy") {
			t.Fatalf("err = %v, want an error naming repo.galaxy", err)
		}
	})
}
