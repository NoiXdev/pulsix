//go:build windows

package dbcheck

import "os"

// keyModeOK accepts every key file: Windows guards files with ACLs, and the
// mode bits Go reports there say nothing about other users.
func keyModeOK(os.FileMode) bool { return true }
