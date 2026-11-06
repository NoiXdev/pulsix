// ShareWorkDir and the group-mirroring it implements: the mechanism that
// lets the play, running under its own uid with the worker's gid as its
// primary group, read and write the files the worker already materialized
// into the run's work dir — without ever granting the world anything.
package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ShareWorkDir walks dir and, for every entry, copies the owner permission
// triplet into the group triplet — directories additionally get setgid, so
// entries the play itself creates under dir inherit the worker's group
// rather than the play's own. World bits are never touched: dir must become
// reachable by the second uid's group, not by anyone else on the host.
//
// Call this only after the caller has finished writing the run's files: it
// mirrors whatever owner bits exist at the time it runs, so writes that
// land afterward keep their own (owner-only) mode until the next call.
//
// dir itself is opened LAST (backlog 7.21). The worker creates it
// owner-only and writes every run file while it still is, so no process of
// the play's group can enter it, plant a symlink for a write to follow, or
// swap an entry between this walk's lstat and its chmod. Opening the root
// first, as the walk's natural order would, ended that guarantee before the
// walk had run.
func ShareWorkDir(dir string) error {
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil // opened last, below
		}
		// os.Chmod always follows a symlink to its target — there is no
		// way to chmod the link itself — so a symlink entry must never
		// reach the Chmod call below. Role trees routinely contain
		// symlinks; computing a mode from the link's own (lstat'd,
		// typically 0777) bits and chmoding through it would set the
		// TARGET's mode, wherever that target lives, including outside
		// dir entirely — exactly the "nothing gains world bits anywhere"
		// invariant this function exists to uphold. A dangling symlink is
		// the same mistake with a different symptom: chmod on a target
		// that does not exist returns ENOENT and would abort the whole
		// walk over an entry that was never the worker's to touch.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		mode := info.Mode()
		perm := mode.Perm()
		// Shift the owner triplet (bits 6-8) down three places to land it on
		// the group triplet, OR it in, and leave every other bit — including
		// the world triplet — exactly as found.
		shared := perm | (perm&0o700)>>3
		if d.IsDir() {
			shared |= os.ModeSetgid
		}
		if err := os.Chmod(path, shared); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
		return nil
	}); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	perm := info.Mode().Perm()
	if err := os.Chmod(dir, perm|(perm&0o700)>>3|os.ModeSetgid); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// ShareRepoRead opens a repo-cache checkout for READING by the worker's
// group: root (the repo cache itself), every directory between root and
// dir, and everything under dir get the owner's read and execute bits
// mirrored into the group triplet — directories 0750, files 0640 — while
// group write is always cleared.
//
// A repo-backed playbook (`repo:<id>:<subpath>`) is never copied into the
// run's work dir; it is executed where it was checked out, under the
// worker's 0700 repo cache, which the play's own uid cannot even traverse.
// Opening it read-only is the whole fix: the play reads its playbook and
// the role tree beside it, and still cannot write there — one job must not
// be able to poison the next job's checkout, which is why group write goes
// even when the clone arrived with it (a worker umask of 002 produces
// 0664 files). The requirements entry the run installed for its commit
// (<repo>.requirements/<key>, next to the clone) is opened the same way on
// every run: the play reads its roles and collections and cannot change
// them for the next run of that commit.
//
// Unlike ShareWorkDir this sets no setgid bit: nothing is created here by
// the play, so there is no group to inherit. Symlinks are skipped for the
// same reason as there — os.Chmod follows them, and a role tree's symlink
// can point anywhere.
//
// The opening is PERMANENT, and that is a property worth saying out loud
// rather than leaving to be discovered (backlog 2.106). Nothing reverts
// these modes when the job ends, so once any repo-backed job has run, the
// cache root and that organization's directory stay 0750 for the life of
// the worker, and every later play can read every checkout opened before
// it. That is inside the trust model rather than a hole in it — all plays
// on one worker run as the SAME uid, so a play that wanted another
// organization's checkout could simply wait for the job that opens it;
// reverting the modes per job would buy a window, not a boundary. The
// boundary for work that must not share a worker is the one the trust
// model already names: a separate zone, and a worker of its own. A
// freshly created cache is 0700 (loop.go's MkdirAll) and stays so until
// the first repo-backed job — which is what makes "the cache is 0700" a
// true statement about a new worker and a false one about a used one.
func ShareRepoRead(root, dir string) error {
	if err := shareAncestors(root, dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return chmodReadShared(path)
	})
}

// shareAncestors is ShareRepoRead's walk down to dir: root and every
// directory between root and dir (not dir itself, unless it is root) get
// group read and traverse and never group write; a symlink on that path is
// refused, naming it.
func shareAncestors(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return fmt.Errorf("locate %s inside the repo cache %s: %w", dir, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s is not inside the repo cache %s", dir, root)
	}
	// The ancestors first: the play needs execute on every directory on
	// the path, or the checkout below is unreachable however open it is.
	//
	// A symlink on that path is refused rather than skipped, which is the
	// opposite of what the WalkDir below does, and the difference is
	// deliberate. A symlink inside a checkout is ordinary repository
	// content: skipping it costs nothing, because the play reaches the
	// target through its own real path. An ancestor is different — it is
	// part of the cache's own structure, which only the worker creates, so
	// a symlink there is something nobody put here on purpose. Skipping it
	// would leave the play unable to traverse and fail the run later with
	// a permission error that names nothing; chmoding it would follow the
	// link and open a directory OUTSIDE the cache, which is worse. So it
	// stops here, naming the path (backlog 2.106).
	//
	// The damage this prevents is Linux-only and therefore invisible on a
	// developer's machine: Linux gives every symlink mode 0777, from which
	// chmodReadShared computes 0757 and chmods the target to world-rwx,
	// while Darwin's 0755 symlinks compute back to 0755 and the chmod is
	// skipped as a no-op. Linux is where the worker runs.
	path := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink on the path into the repo cache %s; opening it to the play's group would follow the link out of the cache", path, root)
		}
		if err := chmodReadShared(path); err != nil {
			return err
		}
		if part == "." {
			break
		}
		path = filepath.Join(path, part)
	}
	return nil
}

// chmodReadShared gives path's group whatever read and execute access its
// owner has, and never write.
func chmodReadShared(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	perm := info.Mode().Perm()
	shared := (perm | (perm&0o500)>>3) &^ 0o020
	if shared == perm {
		return nil
	}
	if err := os.Chmod(path, shared); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}
