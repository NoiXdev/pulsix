//go:build !windows

package secrets

import (
	"os"
	"syscall"
)

// openFile opens path for reading without following a symlink and without
// blocking: a FIFO swapped in after Load's first look would otherwise hold
// the open until something writes to it. Load's same-file check then
// refuses whatever is not the regular file it looked at.
func openFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) // #nosec G304 -- the operator names the file
}
