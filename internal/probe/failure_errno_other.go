//go:build !windows

package probe

import "syscall"

// The errnos behind each failure label on Unix systems.
var (
	refusedErrnos     = []error{syscall.ECONNREFUSED}
	resetErrnos       = []error{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE}
	unreachableErrnos = []error{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN, syscall.EHOSTDOWN}
	timeoutErrnos     = []error{syscall.ETIMEDOUT}
)
