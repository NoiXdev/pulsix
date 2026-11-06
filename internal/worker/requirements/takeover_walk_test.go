package requirements

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTakeOverChecksLinksInTheCopy: the source may change while it is
// copied. A link checked while its path led through a real directory must
// not be published once that directory became a link: roles/a/l -> s/../../x
// stays inside while s is a directory, and leaves the install once s is a
// link to the install's root.
func TestTakeOverChecksLinksInTheCopy(t *testing.T) {
	dir, root, src := takeOverSource(t)
	s := filepath.Join(dir, src)
	if err := os.MkdirAll(filepath.Join(s, "roles/a/s"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkTest(t, "s/../../x", filepath.Join(s, "roles/a/l"))
	swapped := false
	hooks := takeOverHooks{afterCopy: func(p string) {
		if p != "roles/a/l" {
			return
		}
		if err := os.Remove(filepath.Join(s, "roles/a/s")); err != nil {
			t.Errorf("swap: %v", err)
			return
		}
		if err := os.Symlink("../..", filepath.Join(s, "roles/a/s")); err != nil {
			t.Errorf("swap: %v", err)
			return
		}
		swapped = true
	}}

	name, err := takeOverWith(t.Context(), root, src, defaultTakeOverLimits(), hooks)
	if !swapped {
		t.Fatal("the hook never swapped roles/a/s")
	}
	want := "take over the install: roles/a/l: a symlink leaving the install"
	if err == nil || err.Error() != want {
		t.Fatalf("takeOverWith = %q, %v; want %q", name, err, want)
	}
	onlyNames(t, dir, src)
}

// TestTakeOverBoundsTheNamesItHolds: the names listed and not yet copied
// (and the directory paths they hang off) are budgeted, so a tree of very
// many long names cannot exhaust the worker's memory.
func TestTakeOverBoundsTheNamesItHolds(t *testing.T) {
	setup := func(t *testing.T) (string, *os.Root, string) {
		dir, root, src := takeOverSource(t)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			writeTestFile(t, filepath.Join(dir, src, "roles/d", strings.Repeat(n, 40)), n, 0o644)
		}
		writeTestFile(t, filepath.Join(dir, src, "collections/x"), "x", 0o644)
		return dir, root, src
	}
	lim := func(names int) takeOverLimits {
		l := defaultTakeOverLimits()
		l.names = names
		return l
	}

	var peak int
	_, root, src := setup(t)
	if _, err := takeOverWith(t.Context(), root, src, lim(1<<20), takeOverHooks{peak: &peak}); err != nil {
		t.Fatalf("takeOverWith: %v", err)
	}
	// Held at the deepest point: the top names, roles' names and the five
	// names of roles/d, each with its overhead, plus the two directory paths.
	wantPeak := (len("roles") + len("collections") + 2*nameOverhead) +
		(len("roles") + len("d") + nameOverhead) +
		(len("roles/d") + 5*(40+nameOverhead))
	if peak != wantPeak {
		t.Errorf("peak = %d bytes of names, want %d", peak, wantPeak)
	}

	_, root, src = setup(t)
	if _, err := takeOverWith(t.Context(), root, src, lim(wantPeak), takeOverHooks{}); err != nil {
		t.Errorf("takeOverWith within a budget of exactly the peak: %v", err)
	}

	dir, root, src := setup(t)
	_, err := takeOverWith(t.Context(), root, src, lim(wantPeak-1), takeOverHooks{})
	want := "take over the install: roles/d: more than " + strconv.Itoa(wantPeak-1) + " bytes of names to hold"
	if err == nil || err.Error() != want {
		t.Fatalf("takeOverWith one byte under the peak = %v, want %q", err, want)
	}
	onlyNames(t, dir, src)
}

func TestTakeOverEndsWithItsContext(t *testing.T) {
	dir, root, src := takeOverSource(t)
	writeTestFile(t, filepath.Join(dir, src, "roles/acme.web/tasks/main.yml"), "- debug: msg=hi\n", 0o600)
	ctx, cancel := context.WithCancel(t.Context())
	var copied []string
	hooks := takeOverHooks{afterCopy: func(p string) {
		copied = append(copied, p)
		cancel()
	}}
	name, err := takeOverWith(ctx, root, src, defaultTakeOverLimits(), hooks)
	if !errors.Is(err, context.Canceled) || name != "" {
		t.Fatalf("takeOverWith = %q, %v, want context.Canceled", name, err)
	}
	if want := "take over the install: context canceled"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if len(copied) != 1 {
		t.Errorf("copied %q after the cancel, want one entry", copied)
	}
	if got := names(t, dir); len(got) != 1 || got[0] != src {
		t.Errorf("cache holds %v, want only %s", got, src)
	}
}
