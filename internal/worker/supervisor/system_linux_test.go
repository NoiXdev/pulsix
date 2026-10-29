//go:build linux

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// These tests run against real processes. They make the test process a
// child subreaper, which is harmless for a test binary and is the point:
// the orphans below must come back to it. None of them may run in parallel,
// because Reap collects every child of the test process.

// newLinuxSystem returns the real system, already a subreaper, and stops
// and reaps whatever a test leaves behind.
func newLinuxSystem(t *testing.T) system {
	t.Helper()
	sys, err := newSystem()
	if err != nil {
		t.Fatalf("newSystem: %v", err)
	}
	if err := sys.SetSubreaper(); err != nil {
		t.Fatalf("SetSubreaper: %v", err)
	}
	t.Cleanup(func() {
		if err := stopTree(sys, realClock{}, time.Second, stopPoll, maxKillPasses); err != nil {
			t.Errorf("cleanup: stop leftovers: %v", err)
		}
		if _, err := sys.Reap(); err != nil {
			t.Errorf("cleanup: reap: %v", err)
		}
	})
	return sys
}

// statFields reads /proc/<pid>/stat and returns the fields after the
// command name: [0] is the state, [1] the ppid, [3] the session.
func statFields(t *testing.T, pid int) []string {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read stat of %d: %v", pid, err)
	}
	fields, err := parseStat(b)
	if err != nil {
		t.Fatalf("parse stat of %d: %v", pid, err)
	}
	return fields
}

// waitFor polls cond every 10 ms for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLinuxSystemFindsOrphanedGrandchild(t *testing.T) {
	sys := newLinuxSystem(t)
	// The inner shell starts its own session, backgrounds a sleep and exits
	// at once: the sleep is orphaned outside every group and session the
	// test process knows, the 2.107 shape without ansible.
	if _, err := sys.Start([]string{"/bin/sh", "-c", "setsid sh -c 'sleep 300 & ' ; sleep 300"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sid, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("getsid: %v", err)
	}
	ourSession := strconv.Itoa(sid)
	waitFor(t, "the orphaned `sleep 300` of another session among the descendants", func() bool {
		pids, err := sys.Descendants()
		if err != nil {
			t.Fatalf("Descendants: %v", err)
		}
		for _, pid := range pids {
			if cmdline(pid) == "sleep\x00300\x00" && statFields(t, pid)[3] != ourSession {
				return true
			}
		}
		return false
	})
}

// cmdline returns /proc/<pid>/cmdline, or "" for a process that is gone.
func cmdline(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	return string(b)
}

// findDescendant polls Descendants until one has the given cmdline.
func findDescendant(t *testing.T, sys system, want string) int {
	t.Helper()
	var found int
	waitFor(t, "a descendant running "+strings.ReplaceAll(want, "\x00", " "), func() bool {
		pids, err := sys.Descendants()
		if err != nil {
			t.Fatalf("Descendants: %v", err)
		}
		for _, pid := range pids {
			if cmdline(pid) == want {
				found = pid
				return true
			}
		}
		return false
	})
	return found
}

func TestIsDescendantOnRealProcesses(t *testing.T) {
	sys := newLinuxSystem(t)
	self := os.Getpid()
	if isDescendant(self, os.Getppid(), readProcStat) {
		t.Errorf("our parent %d reads as a descendant", os.Getppid())
	}
	child, err := sys.Start([]string{"/bin/sh", "-c", "setsid sh -c 'sleep 301; :' & sleep 300"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !isDescendant(self, child, readProcStat) {
		t.Errorf("started child %d does not read as a descendant", child)
	}
	// sleep 301 runs under an intermediate shell that started its own
	// session and is still alive, so its chain has a link between it and
	// the started child.
	grandchild := findDescendant(t, sys, "sleep\x00301\x00")
	if ppid := statFields(t, grandchild)[1]; ppid == strconv.Itoa(self) || ppid == strconv.Itoa(child) {
		t.Fatalf("sleep 301's parent is %s, want the setsid'd intermediate shell", ppid)
	}
	if !isDescendant(self, grandchild, readProcStat) {
		t.Errorf("grandchild %d under a setsid'd intermediate does not read as a descendant", grandchild)
	}
}

func TestLinuxSystemSignalLeavesAForeignProcessAlone(t *testing.T) {
	sys := newLinuxSystem(t)
	// A process outside our tree: with the subreaper bit cleared for a
	// moment, the shell's backgrounded sleep is orphaned to someone else
	// when the shell exits.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0); err != nil {
		t.Fatalf("clear subreaper: %v", err)
	}
	out, err := exec.Command("/bin/sh", "-c", "sleep 300 >/dev/null 2>&1 & echo $!").Output()
	if err := sys.SetSubreaper(); err != nil {
		t.Fatalf("SetSubreaper: %v", err)
	}
	if err != nil {
		t.Fatalf("start the foreign sleep: %v", err)
	}
	foreign, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("foreign pid %q: %v", out, err)
	}
	// The test may kill directly; only the supervisor is bound to pidfds.
	t.Cleanup(func() { _ = syscall.Kill(foreign, syscall.SIGKILL) })
	if ppid := statFields(t, foreign)[1]; ppid == strconv.Itoa(os.Getpid()) {
		t.Fatalf("the foreign sleep was reparented to the test process; it must not be ours")
	}

	if err := sys.Signal(foreign, syscall.SIGTERM); err != nil {
		t.Errorf("Signal(foreign, SIGTERM) = %v, want nil: a foreign pid is \"not mine\", not an error", err)
	}
	time.Sleep(200 * time.Millisecond) // a delivered SIGTERM ends sleep at once
	if state := statFields(t, foreign)[0]; state == "Z" || state == "X" {
		t.Errorf("the foreign sleep is %s after Signal: the re-check let a signal through", state)
	}
}

func TestLinuxSystemSignalsADescendant(t *testing.T) {
	sys := newLinuxSystem(t)
	pid, err := sys.Start([]string{"/bin/sleep", "300"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := sys.Signal(pid, 0); err != nil {
		t.Errorf("Signal(child, 0) = %v, want nil", err)
	}
	if err := sys.Signal(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("Signal(child, SIGKILL) = %v", err)
	}
	var status syscall.WaitStatus
	waitFor(t, "the killed child to be reaped", func() bool {
		rs, err := sys.Reap()
		if err != nil {
			t.Fatalf("Reap: %v", err)
		}
		for _, r := range rs {
			if r.Pid == pid {
				status = r.Status
				return true
			}
		}
		return false
	})
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Errorf("child status = %#x, want death by SIGKILL", uint32(status))
	}
	// Its pid is gone now: signalling it is success, not an error.
	if err := sys.Signal(pid, syscall.SIGKILL); err != nil {
		t.Errorf("Signal(reaped pid) = %v, want nil", err)
	}
}

func TestLinuxSystemReapLeavesNoZombie(t *testing.T) {
	sys := newLinuxSystem(t)
	pid, err := sys.Start([]string{"/bin/true"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var got *reaped
	waitFor(t, "/bin/true to be reaped", func() bool {
		rs, err := sys.Reap()
		if err != nil {
			t.Fatalf("Reap: %v", err)
		}
		for _, r := range rs {
			if r.Pid == pid {
				got = &r
			}
		}
		return got != nil
	})
	if got.Status.Signaled() || got.Status.ExitStatus() != 0 {
		t.Errorf("status = %#x, want exit 0", uint32(got.Status))
	}
	if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("/proc/%d still exists after Reap (err = %v): a zombie was left behind", pid, err)
	}
}

func TestLinuxSystemReapWithNothingToReap(t *testing.T) {
	sys := newLinuxSystem(t)
	rs, err := sys.Reap()
	if err != nil || len(rs) != 0 {
		t.Errorf("Reap() = %v, %v, want nothing and no error when there are no children", rs, err)
	}
}

func TestLinuxSystemDescendantsSkipsZombies(t *testing.T) {
	sys := newLinuxSystem(t)
	pid, err := sys.Start([]string{"/bin/true"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "/bin/true to become a zombie", func() bool {
		return statFields(t, pid)[0] == "Z"
	})
	pids, err := sys.Descendants()
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if slices.Contains(pids, pid) {
		t.Errorf("Descendants = %v, want the zombie %d left out", pids, pid)
	}
}

func TestNewSystemNamesMissingPidfd(t *testing.T) {
	_, err := newSystemWith(func(int, int) (int, error) { return -1, unix.ENOSYS })
	if err == nil || !strings.Contains(err.Error(), "pidfd") {
		t.Fatalf("err = %v, want a refusal naming the missing pidfd support", err)
	}
	// Both causes are named: an old kernel, and a seccomp profile that
	// blocks the syscalls (an older container runtime's default does), so
	// an operator on a new kernel is not sent to upgrade it.
	for _, cause := range []string{"5.3", "seccomp"} {
		if !strings.Contains(err.Error(), cause) {
			t.Errorf("err = %q, want it to name %q as a possible cause", err, cause)
		}
	}
	if !errors.Is(err, unix.ENOSYS) {
		t.Errorf("err = %v, want it to wrap ENOSYS", err)
	}
}

func TestNewSystemProbesPidfd(t *testing.T) {
	var probed []int
	_, err := newSystemWith(func(pid, flags int) (int, error) {
		probed = append(probed, pid)
		return unix.PidfdOpen(pid, flags)
	})
	if err != nil {
		t.Fatalf("newSystemWith(real pidfd_open): %v", err)
	}
	if !slices.Equal(probed, []int{os.Getpid()}) {
		t.Errorf("probed %v, want exactly one probe of our own pid %d", probed, os.Getpid())
	}
}

// helperEnv makes the test binary act as the supervisor (TestHelperMain):
// Main's exit status and its death by signal can only be seen from outside
// the process. helperCoreEnv additionally lifts the soft core limit to the
// hard one first, so the helper and its runner can dump core at all.
const (
	helperEnv     = "PULSIX_SUPERVISOR_TEST_HELPER"
	helperCoreEnv = "PULSIX_SUPERVISOR_TEST_HELPER_CORE"
)

func TestHelperMain(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process for TestMain*; runs only when re-executed")
	}
	if os.Getenv(helperCoreEnv) == "1" {
		var lim unix.Rlimit
		if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
			t.Fatalf("getrlimit: %v", err)
		}
		lim.Cur = lim.Max
		if err := unix.Setrlimit(unix.RLIMIT_CORE, &lim); err != nil {
			t.Fatalf("setrlimit: %v", err)
		}
	}
	i := slices.Index(os.Args, "--")
	os.Exit(Main(os.Args[i+1:]))
}

// runMain runs Main in a child process on the given shell script and
// returns how that process ended. tweak, when set, adjusts the command
// before it runs.
func runMain(t *testing.T, script string, tweak func(*exec.Cmd)) *os.ProcessState {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperMain$", "--",
		"--grace", "1s", "--", "/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	if tweak != nil {
		tweak(cmd)
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run helper: %v", err)
	}
	if strings.Contains(string(out), ErrPrefix) {
		t.Errorf("supervisor complained: %s", out)
	}
	return cmd.ProcessState
}

func TestMainPassesExitCodeThrough(t *testing.T) {
	for _, code := range []int{0, 2, 4} {
		ps := runMain(t, "exit "+strconv.Itoa(code), nil)
		if got := ps.ExitCode(); got != code {
			t.Errorf("runner exit %d: supervisor exit = %d", code, got)
		}
	}
}

func TestMainDiesOfTheRunnersSignal(t *testing.T) {
	// SIGTERM is one the supervisor itself listens for; SIGUSR1 is one the
	// Go runtime ignores by default, so only a real reset to SIG_DFL ends
	// the process with it.
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGUSR1} {
		ps := runMain(t, "kill -"+strconv.Itoa(int(sig))+" $$", nil)
		ws, ok := ps.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != sig {
			t.Errorf("runner killed by %v: supervisor ended %v, want death by the same signal", sig, ps)
		}
		if ps.ExitCode() >= 0 {
			t.Errorf("runner killed by %v: ExitCode() = %d, want negative as for a signalled runner", sig, ps.ExitCode())
		}
	}
}

func TestMainDumpsNoSecondCore(t *testing.T) {
	dir := t.TempDir()
	ps := runMain(t, "kill -ABRT $$", func(cmd *exec.Cmd) {
		cmd.Dir = dir // where a core_pattern of "core" puts the files
		cmd.Env = append(cmd.Env, helperCoreEnv+"=1")
	})
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGABRT {
		t.Fatalf("runner killed by SIGABRT: supervisor ended %v, want death by the same signal", ps)
	}
	cores, err := filepath.Glob(filepath.Join(dir, "core*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cores) == 0 {
		// Without a core from the runner, the absence of the
		// supervisor's would prove nothing.
		t.Skip("this kernel writes no core files into the working directory (core_pattern); nothing to compare")
	}
	if ws.CoreDump() {
		t.Error("the supervisor dumped core when re-raising the runner's SIGABRT; the runner's core is the only one wanted")
	}
}
