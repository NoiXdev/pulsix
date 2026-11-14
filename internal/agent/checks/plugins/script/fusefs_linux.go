package script

import (
	"fmt"
	"syscall"
)

// fuseSuperMagic is FUSE's file system type.
const fuseSuperMagic = 0x65735546

// onUserMountableFS reports whether dir, a directory or a file, lies on a
// FUSE file system: its server, which a user may run, answers every lookup and
// reports owners and modes as it likes.
func onUserMountableFS(dir string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return false, fmt.Errorf("check %s: %w", dir, err)
	}
	return st.Type == fuseSuperMagic, nil
}
