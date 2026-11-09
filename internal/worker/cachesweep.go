// The repo cache sweep (backlog 7.95): the worker's repo cache keeps a
// clone (<org-id>/<repo-id>), a requirements cache (<repo-id>.requirements)
// and, from workers before the requirements cache, a galaxy directory
// (<repo-id>.galaxy) for every repository it ever synced. Nothing removed
// them when the repository was deleted. Between jobs, at start and then
// every repoCacheSweepInterval (repoListRetry after a failed call), the
// loop asks the manager which repositories the worker's organization still
// has and removes the caches of every other one, and the directories of
// other organizations: a worker belongs to exactly one, so they are left
// from an earlier enrollment on the same data dir.
//
// Without a valid answer nothing is removed: an older manager that does
// not know the call, a failed call and an answer the worker cannot read
// all leave the cache as it is. An answer that lists too little costs a
// fresh clone and install on the next sync, nothing more; the cache holds
// nothing the manager cannot have the worker rebuild.
//
// The sweep removes only what the worker created: real directories whose
// name is a canonical uuid, with one of the suffixes above below the
// organization. A symlink, a regular file or any other name stays. A
// directory is first renamed to a ".removing-*" name and only then
// removed, so a removal cut short never leaves a half-removed clone under
// a repository's name; the next sweep removes the leftover.
//
// The loop runs one job at a time and sweeps between them, so no run uses
// a cache while it goes. A large removal delays the next claim, and a stop
// waits for it; a worker killed during one leaves a ".removing-*" name. Running jobs side by side (backlog 7.93) has to
// keep that true.
package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

const (
	// repoCacheSweepInterval is how long the loop waits after a sweep, or
	// after an answer it cannot sweep with, before the next.
	repoCacheSweepInterval = 6 * time.Hour
	// repoListRetry is how long the loop waits after a failed list call
	// (the manager not reachable yet when the worker starts) before trying
	// again.
	repoListRetry = 5 * time.Minute
	// repoListTimeout bounds the call that lists the repositories.
	repoListTimeout = 30 * time.Second
	// removingPrefix names a directory the sweep is removing.
	removingPrefix = ".removing-"
)

// cacheSuffixes are the names below the organization's directory that
// belong to a repository, besides the clone's own bare <repo-id>.
var cacheSuffixes = []string{".requirements", ".galaxy"}

// sweepRepoCacheIfDue sweeps when no attempt was made yet or the next is
// due.
func (l *Loop) sweepRepoCacheIfDue(ctx context.Context) {
	if !l.cacheSweepDue.IsZero() && l.now().Before(l.cacheSweepDue) {
		return
	}
	l.cacheSweepDue = l.now().Add(l.sweepRepoCache(ctx))
}

// sweepRepoCache lists the organization's repositories and removes the
// caches of every other one, and returns how long to wait before the next
// attempt. Everything it cannot do is logged; nothing stops the loop.
func (l *Loop) sweepRepoCache(ctx context.Context) time.Duration {
	lctx, cancel := context.WithTimeout(ctx, repoListTimeout)
	resp, err := l.client.ListRepositories(lctx, &workerpb.ListRepositoriesRequest{})
	cancel()
	switch {
	case status.Code(err) == codes.Unimplemented:
		l.log.InfoContext(ctx, "the manager does not list repositories yet; caches of deleted repositories stay on this worker until it does")
		return repoCacheSweepInterval
	case err != nil:
		if ctx.Err() == nil {
			l.log.WarnContext(ctx, "could not list the organization's repositories; the repo cache is swept on a later attempt",
				"retry_in", repoListRetry, "err", err)
		}
		return repoListRetry
	}
	org, known, err := readRepoListing(resp)
	if err != nil {
		l.log.WarnContext(ctx, "could not read the manager's repository list; the repo cache is left as it is", "err", err)
		return repoCacheSweepInterval
	}
	removed, err := l.removeStaleRepoCaches(ctx, filepath.Join(l.cfg.DataDir, repoCacheDirName), org, known)
	for _, dir := range removed {
		l.log.InfoContext(ctx, "removed the cache of a repository this worker's organization no longer has", "dir", dir)
	}
	if err != nil {
		l.log.WarnContext(ctx, "could not remove every stale repository cache", "err", err)
	}
	return repoCacheSweepInterval
}

// readRepoListing parses the manager's answer. One unreadable id fails it
// whole: a list missing a repository would remove that repository's cache.
func readRepoListing(resp *workerpb.ListRepositoriesResponse) (uuid.UUID, map[uuid.UUID]bool, error) {
	org, err := uuid.Parse(resp.GetOrgId())
	if err != nil || org == uuid.Nil {
		return uuid.Nil, nil, fmt.Errorf("organization id %q", resp.GetOrgId())
	}
	known := make(map[uuid.UUID]bool, len(resp.GetRepoIds()))
	for _, s := range resp.GetRepoIds() {
		id, err := uuid.Parse(s)
		if err != nil {
			return uuid.Nil, nil, fmt.Errorf("repository id %q", s)
		}
		known[id] = true
	}
	return org, known, nil
}

// canonicalUUID reports the uuid a name is, only in the lower-case
// hyphenated form the worker writes.
func canonicalUUID(name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(name)
	return id, err == nil && id.String() == name
}

// removeStaleRepoCaches removes below cacheRoot the directories of every
// organization but org, and below org's directory the caches of every
// repository not in known, plus ".removing-*" leftovers at both levels.
// It returns the removed paths relative to cacheRoot, sorted. A missing
// cacheRoot or organization directory is nothing to do.
func (l *Loop) removeStaleRepoCaches(ctx context.Context, cacheRoot string, org uuid.UUID, known map[uuid.UUID]bool) ([]string, error) {
	root, err := os.OpenRoot(cacheRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open the repo cache: %w", err)
	}
	defer func() { _ = root.Close() }()

	var removed []string
	var errs []error
	sweep := func(dir *os.Root, rel string, stale func(name string) bool) {
		entries, err := fs.ReadDir(dir.FS(), ".")
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s: %w", filepath.Join(cacheRoot, rel), err))
			return
		}
		for _, e := range entries {
			// ReadDir does not follow symlinks: a link is never a directory here.
			if !e.IsDir() || (!strings.HasPrefix(e.Name(), removingPrefix) && !stale(e.Name())) {
				continue
			}
			if err := l.removeCacheDir(ctx, dir, filepath.Join(cacheRoot, rel), e.Name()); err != nil {
				errs = append(errs, err)
				continue
			}
			removed = append(removed, path.Join(filepath.ToSlash(rel), e.Name()))
		}
	}

	sweep(root, "", func(name string) bool {
		id, ok := canonicalUUID(name)
		return ok && id != org
	})
	if fi, err := root.Lstat(org.String()); err == nil && fi.IsDir() {
		orgRoot, err := root.OpenRoot(org.String())
		if err != nil {
			errs = append(errs, fmt.Errorf("open %s: %w", filepath.Join(cacheRoot, org.String()), err))
		} else {
			sweep(orgRoot, org.String(), func(name string) bool {
				for _, suffix := range cacheSuffixes {
					name = strings.TrimSuffix(name, suffix)
				}
				id, ok := canonicalUUID(name)
				return ok && !known[id]
			})
			_ = orgRoot.Close()
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("look at %s: %w", filepath.Join(cacheRoot, org.String()), err))
	}
	slices.Sort(removed)
	return removed, errors.Join(errs...)
}

// removeCacheDir renames name in dir (at the absolute path parent) to a
// fresh ".removing-*" name, unless it already is one, and removes that.
// What the worker alone cannot remove (an install's leftovers of the play
// uid) is cleared as the play uid, and the worker removes the rest after
// it whatever that pass reported: the play uid cannot unlink the worker's
// own entries, so its rm fails even when it cleared everything it owns.
func (l *Loop) removeCacheDir(ctx context.Context, dir *os.Root, parent, name string) error {
	tmp := name
	if !strings.HasPrefix(name, removingPrefix) {
		tmp = removingPrefix + rand.Text()
		if err := dir.Rename(name, tmp); err != nil {
			return fmt.Errorf("remove %s: %w", filepath.Join(parent, name), err)
		}
	}
	err := dir.RemoveAll(tmp)
	if err != nil && l.cfg.PlayUID != 0 {
		playErr := l.removeAsPlayUID(ctx, l.cfg, filepath.Join(parent, tmp))
		if err = dir.RemoveAll(tmp); err != nil && playErr != nil {
			return fmt.Errorf("remove %s: %w; as the play uid: %w", filepath.Join(parent, name), err, playErr)
		}
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", filepath.Join(parent, name), err)
	}
	return nil
}
