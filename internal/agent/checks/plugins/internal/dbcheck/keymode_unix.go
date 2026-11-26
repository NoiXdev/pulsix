//go:build !windows

package dbcheck

import "os"

// keyModeOK reports whether a key file's mode grants nothing to other
// users. Group access is allowed, so the common 0640 root:ssl-cert layout
// works with the agent in that group.
func keyModeOK(m os.FileMode) bool { return m.Perm()&0o007 == 0 }
