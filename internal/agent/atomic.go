// Atomic file writes for the agent's identity: content lands in a temp
// file in the destination directory, is fsynced and renamed over the
// target, so a reader (or a crash) never sees a torn identity file.
package agent

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes content to dir/name through a temp file in the
// same directory and a rename, so a reader never sees a torn file. The
// mode applies to the final file, not the umask-derived default.
func writeFileAtomic(dir, name string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
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
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
