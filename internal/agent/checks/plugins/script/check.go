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
		return "", contextFor("check "+path, err)
	}
	if err := judgeFile(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// CheckPlace judges the directory that decides whether a file can appear
// at path (placeOf) and every directory above it (judgePlace).
func (fileChecker) CheckPlace(path string) error {
	if err := judgeSyntax(path); err != nil {
		return err
	}
	dir, own, err := placeOf(path)
	if err != nil {
		return contextFor("check the place of "+path, err)
	}
	return judgePlace(dir, own)
}

// contextFor wraps err with what was being done, except a refusal, whose
// text is its reason alone (checker).
func contextFor(doing string, err error) error {
	if _, ok := refusalReason(err); ok {
		return err
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// placeOf is the directory that decides whether a file can appear at
// path, resolved, and whether it is path's own directory. It walks path
// from its root one name at a time, resolving each step where it leads
// (resolveEntry: a link to its target, a ".." from there), so the place
// is judged where the links on the way lead, never where they lie:
//
//   - path exists (as no regular file): the directory it lies in;
//   - a name on the way is missing: the deepest directory reached, its
//     own directory when only the last name is missing;
//   - a name on the way is no directory: the directory that one lies in.
//
// A link on the way that leads to nothing is refused (textDanglingLink).
func placeOf(path string) (dir string, own bool, err error) {
	root, names := splitPlace(path)
	cur, isDir, err := resolveEntry(root)
	if err != nil {
		return "", false, err
	}
	reached := 0
	for _, name := range names {
		if !isDir {
			break
		}
		next, nextIsDir, err := resolveEntry(filepath.Join(cur, name))
		if errors.Is(err, errNotFile) {
			break
		}
		if err != nil {
			return "", false, err
		}
		cur, isDir, reached = next, nextIsDir, reached+1
	}
	switch {
	case reached == len(names):
		return filepath.Dir(cur), true, nil
	case !isDir:
		return filepath.Dir(cur), false, nil
	}
	return cur, reached == len(names)-1, nil
}

// textDanglingLink refuses a path with a link on its way (or at its end)
// that leads to nothing: where it would lead once its target appears is
// not known, so the place cannot be judged.
const textDanglingLink = "a link on its way leads to nothing"
