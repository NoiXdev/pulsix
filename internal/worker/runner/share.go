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
