// Package atomicfile writes a file so a reader, or a crash, never sees a
// torn one: the content lands in a temporary file beside the target, is
// flushed to disk, and is renamed over it.
//
// It exists because three places had grown their own copy of this —
// the agent's identity, the worker's identity and the manager's worker
// bundle — and the copies had already drifted apart in what they
// flushed (backlog 2.64).
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write puts content at dir/name with the given mode, atomically. dir
// must already exist.
//
// Both the file AND the directory are flushed. Flushing only the file
// is the mistake every hand-rolled version here had made: POSIX does
// not promise that a rename is on disk until the PARENT DIRECTORY is
// flushed too, so a crash just after Write returned could leave the
// target missing altogether — not torn, which is what the temp file and
// the rename prevent, but absent, with the old content gone as well. For
// a worker's or an agent's identity that is the difference between
// "reconnects after the power comes back" and "re-enrols, if it still
// can".
//
// mode is applied to the temporary file before the rename, so the
// target never exists with the umask's own permissions, not even
// briefly — these files hold private keys.
func Write(dir, name string, content []byte, mode os.FileMode) error {
	return write(dir, name, content, mode, func(dir, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return f, nil
	})
}

// tempFile is what write needs of the temporary file: *os.File, or a test
// double that fails at a chosen step.
type tempFile interface {
	Chmod(mode os.FileMode) error
	Write(p []byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

// write is Write with the temporary file's creation injected.
func write(dir, name string, content []byte, mode os.FileMode, createTemp func(dir, pattern string) (tempFile, error)) error {
	tmp, err := createTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir flushes the directory entry the rename created. A directory
// that cannot be opened for reading is not an error worth failing the
// write for — the content is already renamed into place, and the only
// thing lost is the durability guarantee.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil //nolint:nilerr // see the comment above
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", dir, err)
	}
	return nil
}
