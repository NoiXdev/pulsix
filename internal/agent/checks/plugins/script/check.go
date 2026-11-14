package script

import "fmt"

// fileChecker is the checker the agent runs with: it resolves the path to
// the file it leads to (resolveFile: symlinks, and on Windows junctions)
// and judges that file, its directory and every directory above it by the
// platform's rules (check_unix.go, check_windows.go). Only the system may
// change a file a command runs or hands its interpreter, or swap it
// through a directory on its way; anyone else who could would run code as
// the agent.
type fileChecker struct{}

func (fileChecker) Check(path string) (string, error) {
	resolved, err := resolveFile(path)
	if err != nil {
		return "", fmt.Errorf("check %s: %w", path, err)
	}
	if err := judgeFile(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}
