//go:build linux

package zfs

import (
	"errors"
	"io/fs"
	"os"
)

// devZFS is the ZFS module's control device; it exists while the module
// is loaded.
const devZFS = "/dev/zfs"

// zfsLoaded reports whether the ZFS module is loaded.
func zfsLoaded() (bool, error) { return pathExists(devZFS) }

// pathExists reports whether p exists; a missing p is no error.
func pathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}
