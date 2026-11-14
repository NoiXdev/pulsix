//go:build windows

package script

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tree is a run's job object. It kills every process in it when its last
// handle closes (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE), and the processes
// the program starts join it on their own. The program is assigned right
// after it starts; whatever it starts in that moment escapes the job.
type tree struct {
	job windows.Handle
}

func newTree() (*tree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create a job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("set up the job object: %w", err)
	}
	return &tree{job: job}, nil
}

// prepare starts the program without a console window: the agent runs as
// a service, where a console would be created for nobody to see.
func (*tree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

// attach assigns the started program to the job. The process is not
// waited on yet, so its pid still names it.
func (t *tree) attach(cmd *exec.Cmd) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open the process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(t.job, h); err != nil {
		return fmt.Errorf("assign the process to its job: %w", err)
	}
	return nil
}

// kill terminates every process in the job (the context's end), and the
// program itself should it not be in the job yet.
func (t *tree) kill(cmd *exec.Cmd) error {
	err := windows.TerminateJobObject(t.job, 1)
	_ = cmd.Process.Kill()
	if err != nil {
		return fmt.Errorf("terminate the job: %w", err)
	}
	return nil
}

// release closes the job, which kills whatever the program left running.
func (t *tree) release(*exec.Cmd) {
	_ = windows.CloseHandle(t.job)
}

// workDir is the directory a command runs in: the root of the drive
// Windows is installed on.
func workDir() (string, error) {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return "", fmt.Errorf("find the system drive: %w", err)
	}
	vol := filepath.VolumeName(dir)
	if vol == "" {
		return "", errors.New("find the system drive: the Windows directory names no drive")
	}
	return vol + `\`, nil
}

// checkProgramName refuses a program whose name has no extension: Windows
// (and os/exec) would look for that name with each of PATHEXT's
// extensions added, a different file from the one the checker checked.
func checkProgramName(prog string) error {
	base := filepath.Base(prog)
	if i := strings.LastIndexByte(base, '.'); i < 0 || i == len(base)-1 {
		return fmt.Errorf("start the program: its name has no extension: %w", windows.ERROR_BAD_EXE_FORMAT)
	}
	return nil
}
