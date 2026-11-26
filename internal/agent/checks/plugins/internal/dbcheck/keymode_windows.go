//go:build windows

package dbcheck

import "os"

// keyModeOK accepts every key file: Windows guards files with ACLs, and the
// mode bits Go reports there say nothing about other users.
func keyModeOK(os.FileMode) bool { return true }

// openTLSFile opens path for reading. Windows has no FIFO a path can name.
func openTLSFile(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- the operator names the file in agent.yaml
}
