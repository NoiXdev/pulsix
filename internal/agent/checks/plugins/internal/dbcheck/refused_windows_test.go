//go:build windows

package dbcheck

import "syscall"

var errConnRefused error = syscall.Errno(wsaeconnrefused)
