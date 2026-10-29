//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linuxSystem is the supervisor's system on Linux: prctl for the subreaper,
// /proc for the tree, pidfds for signals and wait4 for reaping. It holds no
// state; the kernel is the state.
type linuxSystem struct{}

// newSystem returns the Linux system after checking that the kernel has
// pidfds.
func newSystem() (system, error) {
	return newSystemWith(unix.PidfdOpen)
}

// newSystemWith probes pidfd support through open (unix.PidfdOpen outside
// tests) by opening a pidfd on the supervisor itself. Without pidfds (a
// kernel older than 5.3, or a seccomp profile that forbids them) the
// supervisor refuses by name rather than fall back to signalling raw pids,
// which could hit an unrelated process that recycled a pid.
func newSystemWith(open func(pid, flags int) (int, error)) (system, error) {
	fd, err := open(os.Getpid(), 0)
	if err != nil {
		return nil, fmt.Errorf("the play supervisor needs pidfds, which a kernel older than Linux 5.3 lacks and a seccomp profile can block (pidfd_open, pidfd_send_signal): pidfd_open: %w", err)
	}
	_ = unix.Close(fd)
	return linuxSystem{}, nil
}

func (linuxSystem) SetSubreaper() error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_CHILD_SUBREAPER): %w", err)
	}
	return nil
}

// Start forks and execs argv with the supervisor's fds 0-2 and environment.
// It sets no process group of its own: the runner stays in the group the
// worker created for the supervisor, so the worker's TERM to that group
// still reaches ansible-runner exactly as before the supervisor existed.
// syscall.ForkExec rather than os.StartProcess or exec.Cmd, because nothing
// but Reap may wait for the child.
func (linuxSystem) Start(argv []string) (int, error) {
	pid, err := syscall.ForkExec(argv[0], argv, &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2},
	})
	if err != nil {
		return 0, fmt.Errorf("fork/exec: %w", err)
	}
	return pid, nil
}

func (linuxSystem) DropStdout() error {
	null, err := unix.Open(os.DevNull, unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer func() { _ = unix.Close(null) }()
	if err := unix.Dup3(null, 1, 0); err != nil {
		return fmt.Errorf("dup3 %s onto stdout: %w", os.DevNull, err)
	}
	return nil
}

// Descendants lists /proc and walks it from the supervisor (descendantsFrom).
// Only a failure to list /proc itself is an error; a single entry that
// cannot be read is skipped by the walk.
func (linuxSystem) Descendants() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid) // the rest is not a process: self, sys, meminfo, …
		}
	}
	return descendantsFrom(os.Getpid(), pids, readProcStat), nil
}

// readProcStat is the statReader over the real /proc.
func readProcStat(pid int) ([]byte, error) {
	return os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
}

// Signal sends sig through a pidfd, never by raw pid. The pidfd pins the
// process: once it is open, it names this process and no other, even if the
// pid is recycled. Descent is therefore re-checked after opening it, by
// walking pid's own parent chain (isDescendant); a pid that was reaped and
// reused between the caller's Descendants and here either fails to open or
// is no longer a descendant, and is left alone, as is one whose chain cannot
// be read.
func (linuxSystem) Signal(pid int, sig syscall.Signal) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pidfd_open: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	if !isDescendant(os.Getpid(), pid, readProcStat) {
		return nil
	}
	if err := unix.PidfdSendSignal(fd, sig, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("pidfd_send_signal: %w", err)
	}
	return nil
}

// Reap collects every ended child with wait4(-1, WNOHANG) until there is
// nothing more to collect. As a subreaper the supervisor is handed every
// orphan of the play, so it reaps them all, not only the runner.
func (linuxSystem) Reap() ([]reaped, error) {
	var out []reaped
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return out, nil // no children at all
		case err != nil:
			return out, fmt.Errorf("wait4: %w", err)
		case pid == 0:
			return out, nil // children, but none has ended
		}
		out = append(out, reaped{Pid: pid, Status: syscall.WaitStatus(ws)})
	}
}

// raise ends the supervisor with sig, as the runner ended. The Go runtime
// handles most signals itself (and signal.Reset only restores the runtime's
// handling, under which a SIGUSR1 is ignored and a SIGSEGV becomes a crash
// with exit code 2), so the disposition is set straight to SIG_DFL with
// rt_sigaction, the signal is unblocked on this thread, and it is sent to
// this thread, where the kernel delivers it on the way back from tgkill.
// The core size limit is set to zero first: when the runner died of a
// dumping signal (SIGABRT, SIGSEGV, …) it has already left its own core, and
// the supervisor's would be a second one of a process that did not crash.
// The worker still sees the same signal; only the core-dumped bit differs.
// It returns only if sig does not end a process by default.
func raise(sig syscall.Signal) error {
	runtime.LockOSThread()
	// A failure here must not stop the raise: dying of the right signal
	// matters more than a spare core file. It is reported only if the
	// process survives.
	var errs []error
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		errs = append(errs, fmt.Errorf("setrlimit(RLIMIT_CORE, 0): %w", err))
	}
	// SIGKILL's disposition cannot be changed, and need not be.
	if sig != syscall.SIGKILL {
		// A zeroed kernel struct sigaction is SIG_DFL with no flags and
		// an empty mask on every architecture; 32 bytes covers its size
		// on all of them.
		var act [4]uint64
		if _, _, errno := unix.RawSyscall6(unix.SYS_RT_SIGACTION, uintptr(sig),
			uintptr(unsafe.Pointer(&act)), 0, 8, 0, 0); errno != 0 {
			return fmt.Errorf("rt_sigaction(SIG_DFL): %w", errno)
		}
	}
	var set unix.Sigset_t
	bits := uint(unsafe.Sizeof(set.Val[0])) * 8
	n := uint(sig) - 1
	set.Val[n/bits] |= 1 << (n % bits)
	if err := unix.PthreadSigmask(unix.SIG_UNBLOCK, &set, nil); err != nil {
		return fmt.Errorf("unblock: %w", err)
	}
	if err := unix.Tgkill(unix.Getpid(), unix.Gettid(), sig); err != nil {
		errs = append(errs, fmt.Errorf("tgkill: %w", err))
	}
	return errors.Join(errs...)
}
