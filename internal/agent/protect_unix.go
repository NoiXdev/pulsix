//go:build !windows

package agent

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// unixProtector applies the Unix rule: a secret file belongs to the
// agent's effective user (root under systemd and launchd) and nobody else
// may read or write it. Directories are never reported: the agent creates
// its data directory 0700 and writes every file in it 0600, so the
// directory's own mode was never a gate and stays that way.
type unixProtector struct{ euid int }

func newProtector() protector { return unixProtector{euid: os.Geteuid()} }

func (u unixProtector) Loose(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if fi.IsDir() {
		return "", nil
	}
	var found []string
	if owner := u.fileOwner(fi); owner != "" {
		found = append(found, owner)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		found = append(found, fmt.Sprintf("mode %04o", perm))
	}
	return strings.Join(found, ", "), nil
}

// ForeignOwner is "" for a directory (the data directory's owner was never
// a gate on Unix) and reports a file's owner when it is not the agent's
// effective user.
func (u unixProtector) ForeignOwner(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if fi.IsDir() {
		return "", nil
	}
	return u.fileOwner(fi), nil
}

func (u unixProtector) fileOwner(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int64(st.Uid) != int64(u.euid) {
		return fmt.Sprintf("owner uid %d", st.Uid)
	}
	return ""
}

// Protect drops every group and other bit: 0700 for a directory, 0600 for
// a file (a secret file has no business being executable).
func (unixProtector) Protect(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	mode := os.FileMode(0o600)
	if fi.IsDir() {
		mode = 0o700
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return nil
}
