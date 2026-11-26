//go:build !windows

package dbcheck

import (
	"os"
	"syscall"
)

// keyModeOK reports whether a key file's mode grants nothing to other
// users. Group access is allowed, so the common 0640 root:ssl-cert layout
// works with the agent in that group.
func keyModeOK(m os.FileMode) bool { return m.Perm()&0o007 == 0 }

// openTLSFile opens path for reading without blocking: a FIFO would
// otherwise hold the open until something writes to it. A regular file
// ignores O_NONBLOCK, and readTLSFile refuses anything else.
func openTLSFile(path string) (*os.File, error) {
	// O_NOCTTY: a terminal device named here never becomes the agent's
	// controlling terminal.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0) // #nosec G304 -- the operator names the file in agent.yaml
}
