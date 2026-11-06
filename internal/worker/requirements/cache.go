package requirements

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// maxEntries is how many published entries a repo's cache keeps: the commit
// being run, the previous one and one rollback (spec goal 1).
const maxEntries = 3

// entryName is the name of a published entry: <commit>-<hash16>, the commit a
// full SHA-1 or SHA-256.
var entryName = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})-[0-9a-f]{16}$`)

// tempPrefix starts the name of an install in progress.
const tempPrefix = ".tmp-"

// mkTempAttempts bounds how often mkTemp draws a new name after a collision.
const mkTempAttempts = 10

// CacheDir is the requirements cache directory of the clone at cloneDir:
// <parent>/<base>.requirements, next to the clone and outside its working tree.
func CacheDir(cloneDir string) string {
	clean := filepath.Clean(cloneDir)
	return filepath.Join(filepath.Dir(clean), filepath.Base(clean)+".requirements")
}

// mkTemp creates a fresh temporary directory ".tmp-<16 hex>" (0700) in the
// cache directory that root is opened on and returns its name.
func mkTemp(root *os.Root) (string, error) {
	return mkTempWith(root, rand.Reader)
}

// mkTempWith is mkTemp drawing its names from random.
func mkTempWith(root *os.Root, random io.Reader) (string, error) {
	name, err := withTempName(random, func(name string) error { return root.Mkdir(name, 0o700) })
	if err != nil {
		return "", fmt.Errorf("make temporary requirements directory: %w", err)
	}
	return name, nil
}

// withTempName draws a name ".tmp-<16 hex>" from random and calls try with
// it. A name try reports as existing (fs.ErrExist) is drawn again, up to
// mkTempAttempts times.
func withTempName(random io.Reader, try func(name string) error) (string, error) {
	buf := make([]byte, 8)
	for range mkTempAttempts {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", err
		}
		name := tempPrefix + hex.EncodeToString(buf)
		err := try(name)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("%d temporary names were taken", mkTempAttempts)
}

// sweepTemp removes every ".tmp-*" name in the cache directory that root is
// opened on: an install a killed worker left behind. The caller holds the lock,
// so no install is in progress. A symlink is removed as the link, never its
// target. What the worker cannot remove (files the play uid owns) is handed to
// fallback by name, and then removed again. Every name that stays is reported;
// the others are still swept.
func sweepTemp(root *os.Root, fallback func(name string) error) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("sweep temporary requirements directories: %w", err)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, tempPrefix) {
			continue
		}
		if err := removeTemp(root, name, fallback); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeTemp removes one temporary directory, through fallback when the
// worker alone cannot.
func removeTemp(root *os.Root, name string, fallback func(name string) error) error {
	err := root.RemoveAll(name)
	if err == nil {
		return nil
	}
	if fallback == nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	if ferr := fallback(name); ferr != nil {
		return fmt.Errorf("remove %s: %w; as the play uid: %w", name, err, ferr)
	}
	if err := root.RemoveAll(name); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// evict removes the published entries of the cache directory that root is
// opened on beyond the keep most recently used: real directories named like
// entryName, newest mtime first, ties broken by name. The entry named inUse,
// the one the caller is about to return, counts as the most recent whatever
// its mtime says, and is never removed. Nothing else is looked at; an
// entry-named symlink or file stays (spec goal 1a).
//
// A victim is first renamed to a fresh ".tmp-*" name (atomic) and only then
// removed, so a removal that fails part-way or a worker killed during it never
// leaves a half-removed directory under an entry name, where it would be used
// as a valid entry. The leftover is a ".tmp-*" that the next sweepTemp removes.
func evict(root *os.Root, keep int, inUse string) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("evict requirements entries: %w", err)
	}
	var candidates []string
	for _, e := range entries {
		if entryName.MatchString(e.Name()) {
			candidates = append(candidates, e.Name())
		}
	}
	found, err := usedEntries(root, candidates)
	if err != nil {
		return fmt.Errorf("evict requirements entries: %w", err)
	}
	slices.SortFunc(found, func(a, b usedEntry) int {
		switch {
		case a.name == inUse:
			return -1
		case b.name == inUse:
			return 1
		}
		if c := b.mtime.Compare(a.mtime); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	var errs []error
	for _, u := range found[min(max(keep, 0), len(found)):] {
		if u.name == inUse {
			continue
		}
		if err := removeEntry(root, u.name); err != nil {
			errs = append(errs, fmt.Errorf("evict %s: %w", u.name, err))
		}
	}
	return errors.Join(errs...)
}

// removeEntry renames the entry name to a fresh temporary name, then removes
// that.
func removeEntry(root *os.Root, name string) error {
	tmp, err := withTempName(rand.Reader, func(tmp string) error {
		if _, err := root.Lstat(tmp); err == nil {
			return fs.ErrExist
		}
		return root.Rename(name, tmp)
	})
	if err != nil {
		return fmt.Errorf("rename to a temporary name: %w", err)
	}
	if err := root.RemoveAll(tmp); err != nil {
		return fmt.Errorf("remove %s: %w", tmp, err)
	}
	return nil
}

// usedEntry is a published entry and when it was last used.
type usedEntry struct {
	name  string
	mtime time.Time
}

// usedEntries returns the names that are real directories, with their
// mtimes. A name that is gone by the time it is looked at is skipped.
func usedEntries(root *os.Root, names []string) ([]usedEntry, error) {
	var found []usedEntry
	for _, name := range names {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			found = append(found, usedEntry{name, fi.ModTime()})
		}
	}
	return found, nil
}

// removeLegacy removes <clone>.galaxy, the galaxy directory of workers before
// the requirements cache (spec goal 8), through a root on the clone's parent:
// a directory with its contents, a symlink as the link only, never its target.
// Nothing there is not an error.
func removeLegacy(cloneDir string) error {
	clean := filepath.Clean(cloneDir)
	name := filepath.Base(clean) + ".galaxy"
	parent, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return fmt.Errorf("remove %s: %w", filepath.Join(filepath.Dir(clean), name), err)
	}
	defer func() { _ = parent.Close() }()
	if err := parent.RemoveAll(name); err != nil {
		return fmt.Errorf("remove %s: %w", filepath.Join(filepath.Dir(clean), name), err)
	}
	return nil
}
