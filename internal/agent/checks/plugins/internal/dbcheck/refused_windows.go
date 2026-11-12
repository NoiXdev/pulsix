//go:build windows

package dbcheck

import (
	"errors"
	"syscall"
)

// wsaeconnrefused is WSAECONNREFUSED; syscall.ECONNREFUSED on Windows is a
// placeholder value Winsock never returns.
const wsaeconnrefused = 10061

func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(wsaeconnrefused))
}
