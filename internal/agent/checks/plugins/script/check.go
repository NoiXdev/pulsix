package script

import (
	"errors"
	"fmt"
	"path/filepath"
)

// fileChecker is the checker the agent runs with: it resolves the path to
// the file it leads to (resolveFile: symlinks, and on Windows junctions)
// and judges that file, its directory and every directory above it by the
// platform's rules (check_unix.go, check_windows.go). Only the system may
// change a file a command runs or hands its interpreter, or swap it
// through a directory on its way; anyone else who could would run code as
// the agent.
type fileChecker struct{}

func (fileChecker) Check(path string) (string, error) {
	if err := judgeSyntax(path); err != nil {
		return "", err
	}
	resolved, err := resolveFile(path)
	if err != nil {
		return "", fmt.Errorf("check %s: %w", path, err)
	}
	if err := judgeFile(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// CheckPlace judges the deepest directory on path's way that exists
// (placeOf) and every directory above it (judgePlace).
func (fileChecker) CheckPlace(path string) error {
	if err := judgeSyntax(path); err != nil {
		return err
	}
	dir, own, err := placeOf(path)
	if err != nil {
		return fmt.Errorf("check the place of %s: %w", path, err)
	}
	return judgePlace(dir, own)
}

// placeOf is the deepest directory on path's way that exists, resolved
// (resolveDir), and whether it is path's own directory: path itself names
// no regular file, so its directory comes first, then each one above
// while one is missing or no directory.
func placeOf(path string) (dir string, own bool, err error) {
	own = true
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		resolved, err := resolveDir(d)
		switch {
		case err == nil:
			return resolved, own, nil
		case !errors.Is(err, errNotFile):
			return "", false, err
		case filepath.Dir(d) == d:
			return "", false, errors.New("no directory on its way exists")
		}
		own = false
	}
}
