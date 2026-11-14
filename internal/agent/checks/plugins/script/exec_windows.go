//go:build windows

package script

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tree is a run's job object. It kills every process in it when its last
// handle closes (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE), and the processes
// the program starts join it on their own. The program starts suspended,
// is assigned to the job, and only then runs, so nothing it starts
// escapes the job.
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

// prepare starts the program suspended (attach resumes it) and without a
// console window: the agent runs as a service, where a console would be
// created for nobody to see.
func (*tree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED}
}

// processSuspendResume is PROCESS_SUSPEND_RESUME, which x/sys/windows
// does not define.
const processSuspendResume = 0x0800

// ntResumeProcess resumes every thread of a process; a process started
// suspended has one, whose handle os/exec does not keep.
var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// attach assigns the started, still suspended program to the job, then
// lets it run. The process is not waited on yet, so its pid still names
// it.
func (t *tree) attach(cmd *exec.Cmd) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|processSuspendResume,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open the process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(t.job, h); err != nil {
		return fmt.Errorf("assign the process to its job: %w", err)
	}
	if err := ntResumeProcess.Find(); err != nil {
		return fmt.Errorf("resume the process: %w", err)
	}
	if st, _, _ := ntResumeProcess.Call(uintptr(h)); st != 0 {
		return fmt.Errorf("resume the process: %w", windows.NTStatus(st))
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
		return errNoExtension
	}
	return nil
}

// fileArgument says whether arg names a file the checker must pass, and
// by which path. Besides an absolute path, Windows reads a path rooted
// without a drive (`\scripts\x.ps1`, also with forward slashes) on the
// working directory's drive, and a path with a drive but no root (`C:x`)
// from that drive's working directory, which for a command is the drive's
// root: the system drive's is its working directory, and it inherits no
// other. A path that starts with two separators is decided by its syntax
// alone, never stat'ed (windowsPathKindOf): a device or namespace path, or
// \\x without a share (a switch like //Nologo), names no file to check;
// a network path goes to the checker, which refuses it.
func fileArgument(arg string) (string, bool, error) {
	switch windowsPathKindOf(arg) {
	case pathDevice, pathNoShare:
		return "", false, nil
	case pathNetwork:
		return arg, true, nil
	}
	if filepath.IsAbs(arg) {
		return arg, true, nil
	}
	vol := filepath.VolumeName(arg)
	rest := arg[len(vol):]
	switch {
	case vol != "":
		return vol + `\` + rest, true, nil
	case rest != "" && os.IsPathSeparator(rest[0]):
		dir, err := workDir()
		if err != nil {
			return "", true, err
		}
		return filepath.VolumeName(dir) + rest, true, nil
	}
	return "", false, nil
}
