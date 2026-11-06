package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// prepareInstall opens what an install as the play uid needs to its group
// (spec, owner decision 2026-11-06), and nothing else in the cache:
//
//   - the clone, read-only (ShareRepoRead): galaxy reads the local sources;
//   - every directory from RepoCacheDir down to and including the temp
//     entry and its input/: group read and traverse, never write, a
//     symlink on the path refused — so a "../" name out of roles/ or
//     collections/ fails with "Permission denied";
//   - input/'s files (the requirements copies and galaxy.cfg): group read;
//   - home/, home/.ssh/, tmp/, roles/, collections/: owner bits mirrored
//     into the group, setgid (2770) — the only places the play uid writes.
//
// The four writable directories are opened themselves only, never walked:
// the second command of a file (its collections after its roles) finds the
// first one's files there, owned by the play uid, which the worker could
// not chmod, and must not follow a link the play uid planted.
func prepareInstall(in requirements.Install) error {
	if err := ShareRepoRead(in.RepoCacheDir, in.CloneDir); err != nil {
		return fmt.Errorf("open the clone to the play's group: %w", err)
	}
	input := filepath.Join(in.Entry, "input")
	if err := shareAncestors(in.RepoCacheDir, input); err != nil {
		return fmt.Errorf("open the requirements install to the play's group: %w", err)
	}
	if err := shareInput(input); err != nil {
		return fmt.Errorf("open the requirements install to the play's group: %w", err)
	}
	for _, d := range []string{"home", filepath.Join("home", ".ssh"), "tmp", "roles", "collections"} {
		if err := shareWritable(filepath.Join(in.Entry, d)); err != nil {
			return fmt.Errorf("open the requirements install to the play's group: %w", err)
		}
	}
	return nil
}

// shareInput gives dir and its files group read (and traverse for dir),
// never write. Only the worker writes there, so anything but a regular
// file is refused.
func shareInput(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err := chmodReadShared(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		if err := chmodReadShared(path); err != nil {
			return err
		}
	}
	return nil
}

// shareWritable mirrors the directory dir's owner bits into its group and
// sets setgid, through a descriptor opened without following a link: dir
// may sit in a directory the play uid can write to.
func shareWritable(dir string) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%s is a symlink; opening it to the play's group would follow the link", dir)
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	perm := info.Mode().Perm()
	shared := perm | (perm&0o700)>>3
	if shared == perm && info.Mode()&os.ModeSetgid != 0 {
		return nil
	}
	if err := f.Chmod(shared | os.ModeSetgid); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}
