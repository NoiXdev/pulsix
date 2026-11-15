package zfs

import (
	"errors"
	"fmt"
	"path"
)

// errNotFound is a ZFS program that is not installed.
var errNotFound = errors.New("not found")

// standardDirs are where the distributions' and a source build's ZFS
// programs live, tried in order before PATH.
var standardDirs = []string{"/usr/sbin", "/sbin", "/usr/local/sbin"}

// locator finds a ZFS program. The agent runs it as root, so the standard
// locations — absolute paths only root can write — are tried before the
// agent's PATH, which is only the fallback.
type locator struct {
	lookPath func(string) (string, error) // exec.LookPath
}

// find returns the program to run: the first standard location that holds
// an executable, else the one on PATH. lookPath checks an absolute
// candidate in place; whatever is found must be an absolute path.
func (l locator) find(name string) (string, error) {
	candidates := make([]string, 0, len(standardDirs)+1)
	for _, dir := range standardDirs {
		candidates = append(candidates, path.Join(dir, name))
	}
	candidates = append(candidates, name)
	for _, c := range candidates {
		if p, err := l.lookPath(c); err == nil && path.IsAbs(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s %w", name, errNotFound)
}
