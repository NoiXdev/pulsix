//go:build windows

package secrets

import "os"

// openFile opens path for reading. Windows has no FIFO a path can be
// swapped for, and Load's same-file check refuses a link or any other file
// than the one it looked at.
func openFile(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- the operator names the file
}
