package script

import (
	"fmt"
	"syscall"
)

// procSuperMagic is procfs's file system type.
const procSuperMagic = 0x9fa0

// onKernelFS reports whether the directory dir lies on procfs, whose
// entries the kernel makes up per process (/proc/self/fd/2 is the
// reader's own standard error): no file anyone could put there.
func onKernelFS(dir string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return false, fmt.Errorf("check %s: %w", dir, err)
	}
	return st.Type == procSuperMagic, nil
}
