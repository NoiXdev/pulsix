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

// mkTempWith is mkTemp drawing its names from random. A name that already
// exists is drawn again, up to mkTempAttempts times.
func mkTempWith(root *os.Root, random io.Reader) (string, error) {
	buf := make([]byte, 8)
	for range mkTempAttempts {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", fmt.Errorf("make temporary requirements directory: %w", err)
		}
		name := tempPrefix + hex.EncodeToString(buf)
		err := root.Mkdir(name, 0o700)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("make temporary requirements directory: %w", err)
		}
	}
	return "", fmt.Errorf("make temporary requirements directory: %d names were taken", mkTempAttempts)
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
// entryName, newest mtime first, ties broken by name. Nothing else is looked
// at; an entry-named symlink or file stays (spec goal 1a).
func evict(root *os.Root, keep int) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("evict requirements entries: %w", err)
	}
	type used struct {
		name  string
		mtime time.Time
	}
	var found []used
	for _, e := range entries {
		if !entryName.MatchString(e.Name()) {
			continue
		}
		fi, err := root.Lstat(e.Name())
		if err != nil {
			return fmt.Errorf("evict requirements entries: %w", err)
		}
		if !fi.IsDir() {
			continue
		}
		found = append(found, used{e.Name(), fi.ModTime()})
	}
	slices.SortFunc(found, func(a, b used) int {
		if c := b.mtime.Compare(a.mtime); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	var errs []error
	for _, u := range found[min(max(keep, 0), len(found)):] {
		if err := root.RemoveAll(u.name); err != nil {
			errs = append(errs, fmt.Errorf("evict %s: %w", u.name, err))
		}
	}
	return errors.Join(errs...)
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
