//go:build !linux && !windows

package script

// onUserMountableFS reports no user-mountable file system: only Linux's
// FUSE is recognised.
func onUserMountableFS(string) (bool, error) { return false, nil }
