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
func ShareWorkDir(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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
	})
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
// 0664 files).
//
// Unlike ShareWorkDir this sets no setgid bit: nothing is created here by
// the play, so there is no group to inherit. Symlinks are skipped for the
// same reason as there — os.Chmod follows them, and a role tree's symlink
// can point anywhere.
func ShareRepoRead(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return fmt.Errorf("locate %s inside the repo cache %s: %w", dir, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s is not inside the repo cache %s", dir, root)
	}
	// The ancestors first: the play needs execute on every directory on
	// the path, or the checkout below is unreachable however open it is.
	path := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if err := chmodReadShared(path); err != nil {
			return err
		}
		if part == "." {
			break
		}
		path = filepath.Join(path, part)
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
