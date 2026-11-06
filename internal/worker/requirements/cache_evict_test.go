package requirements

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

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

	if err := evict(openTestRoot(t, dir), maxEntries, ""); err != nil {
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
	if err := evict(root, 5, ""); err != nil {
		t.Fatalf("evict above the count: %v", err)
	}
	if got := len(names(t, dir)); got != 3 {
		t.Fatalf("evict above the count left %d entries, want 3", got)
	}
	if err := evict(root, 1, ""); err != nil {
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
	root := openTestRoot(t, dir)
	err := evict(root, 1, "")
	if err == nil || !strings.Contains(err.Error(), old) {
		t.Fatalf("err = %v, want it to name %s", err, old)
	}

	// The half-removed entry is no longer under its entry name: only a
	// .tmp-* is left, so it can never be used as a valid entry.
	left := names(t, dir)
	if len(left) != 2 || left[1] != young || !regexp.MustCompile(`^\.tmp-[0-9a-f]{16}$`).MatchString(left[0]) {
		t.Fatalf("left %v, want a .tmp-* and %s", left, young)
	}
	stuck := left[0]
	if !strings.Contains(err.Error(), stuck) {
		t.Fatalf("err = %v, want it to name %s too", err, stuck)
	}

	// The next sweep removes it with the fallback.
	err = sweepTemp(root, func(name string) error {
		return os.Chmod(filepath.Join(dir, name, "roles"), 0o700)
	})
	if err != nil {
		t.Fatalf("sweepTemp: %v", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{young}) {
		t.Fatalf("left %v, want only %s", got, young)
	}
}

func TestEvictNeverRemovesTheEntryInUse(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	// A clock skewed into the past: the entry just used carries the oldest
	// mtime of all.
	mtimes := map[byte]time.Time{1: base.Add(-24 * time.Hour)}
	for i := byte(2); i <= 5; i++ {
		mtimes[i] = base.Add(time.Duration(i) * time.Minute)
	}
	for i, at := range mtimes {
		mkdirs(t, dir, entryKey(i)+"/roles")
		if err := os.Chtimes(filepath.Join(dir, entryKey(i)), at, at); err != nil {
			t.Fatal(err)
		}
	}
	root := openTestRoot(t, dir)
	if err := evict(root, maxEntries, entryKey(1)); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if got, want := names(t, dir), []string{entryKey(1), entryKey(4), entryKey(5)}; !slices.Equal(got, want) {
		t.Fatalf("left %v, want the entry in use and the two newest %v", got, want)
	}
	if err := evict(root, 0, entryKey(1)); err != nil {
		t.Fatalf("evict to zero: %v", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{entryKey(1)}) {
		t.Fatalf("left %v, want the entry in use", got)
	}
}

func TestUsedEntriesSkipsAnEntryThatIsGone(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, entryKey(1))
	got, err := usedEntries(openTestRoot(t, dir), []string{entryKey(1), entryKey(2)})
	if err != nil {
		t.Fatalf("usedEntries: %v", err)
	}
	if len(got) != 1 || got[0].name != entryKey(1) {
		t.Fatalf("usedEntries = %v, want only %s", got, entryKey(1))
	}
}

func TestEvictLeavesAnEntryItCannotRenameWhole(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root renames in a read-only directory")
	}
	dir := t.TempDir()
	old, young := entryKey(1), entryKey(2)
	mkdirs(t, dir, old+"/roles/r", young)
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, old), t0, t0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, young), t0.Add(time.Minute), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, dir)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := evict(root, 1, "")
	if err == nil || !strings.Contains(err.Error(), old) {
		t.Fatalf("err = %v, want it to name %s", err, old)
	}
	if got := names(t, dir); !slices.Equal(got, []string{old, young}) {
		t.Fatalf("left %v, want both entries", got)
	}
	if _, err := os.Stat(filepath.Join(dir, old, "roles", "r")); err != nil {
		t.Fatalf("the entry that could not be renamed changed: %v", err)
	}
}
