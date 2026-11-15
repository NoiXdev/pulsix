//go:build !linux

package zfs

import "errors"

var errUnsupported = errors.New("not supported on this platform")

// zfsLoaded off Linux: the plugin reads Linux hosts' pools only.
func zfsLoaded() (bool, error) { return false, errUnsupported }
