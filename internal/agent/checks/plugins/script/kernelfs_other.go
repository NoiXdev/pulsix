//go:build !linux && !windows

package script

// onKernelFS reports no kernel file system: only Linux's /proc is one the
// walk stops at.
func onKernelFS(string) (bool, error) { return false, nil }
