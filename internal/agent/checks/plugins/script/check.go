package script

import (
	"fmt"
	"os"
	"path/filepath"
)

// fileChecker is the checker the agent runs with: it resolves symlinks
// and judges the file they lead to and that file's directory by the
// platform's rules (check_unix.go, check_windows.go). Only the system may
// change a file a command runs or hands its interpreter; anyone else who
// could would run code as the agent.
type fileChecker struct{}

func (fileChecker) Check(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if namesNoFile(err) {
			return fmt.Errorf("check %s: %w", path, errNotFile)
		}
		return fmt.Errorf("check %s: %w", path, err)
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		if namesNoFile(err) {
			return fmt.Errorf("check %s: %w", path, errNotFile)
		}
		return fmt.Errorf("check %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("check %s: %w", path, errNotFile)
	}
	return judgeFile(resolved, fi)
}
