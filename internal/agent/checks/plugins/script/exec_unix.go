//go:build !windows

package script

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// tree is a run's process group: the program starts as the leader of a
// new group (Setpgid), and its descendants stay in it unless they leave
// on purpose (setsid), which a check script has no reason to.
type tree struct{}

func newTree() (*tree, error) { return &tree{}, nil }

func (*tree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// attach has nothing to do: the group exists from the start.
func (*tree) attach(*exec.Cmd) error { return nil }

// kill kills the whole group (the context's end).
func (*tree) kill(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// release kills whatever is left of the group once Wait has reaped the
// program, so nothing it started outlives the run (on Windows closing the
// job does the same). The group's id is the program's pid, and neither
// Linux nor the BSDs hand out a pid that is still some group's id, so
// while one member remains the signal reaches only this group. Once none
// does it finds no group, unless in the microseconds since the reap a new
// process took that pid and made itself a group leader. cmd is nil when
// the program never started.
func (*tree) release(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// workDir is the directory a command runs in: the root.
func workDir() (string, error) { return "/", nil }

// checkProgramName accepts every program path: Unix runs exactly the file
// named.
func checkProgramName(string) error { return nil }
