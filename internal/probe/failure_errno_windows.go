//go:build windows

package probe

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// The errnos behind each failure label on Windows: Winsock's, and the
// Unix-named ones the syscall package defines for portable code.
var (
	refusedErrnos     = []error{windows.WSAECONNREFUSED, windows.ERROR_CONNECTION_REFUSED, syscall.ECONNREFUSED}
	resetErrnos       = []error{windows.WSAECONNRESET, windows.WSAECONNABORTED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE}
	unreachableErrnos = []error{windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH, windows.WSAENETDOWN, windows.WSAEHOSTDOWN, windows.ERROR_NETWORK_UNREACHABLE, windows.ERROR_HOST_UNREACHABLE, syscall.ENETUNREACH, syscall.EHOSTUNREACH}
	timeoutErrnos     = []error{windows.WSAETIMEDOUT, syscall.ETIMEDOUT}
)
