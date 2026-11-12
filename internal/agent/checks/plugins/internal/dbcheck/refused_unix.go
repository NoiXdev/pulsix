//go:build !windows

package dbcheck

import (
	"errors"
	"syscall"
)

func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
