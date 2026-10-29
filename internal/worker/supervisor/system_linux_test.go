//go:build linux

package supervisor

import (
	"errors"
	"os"
	"os/exec"
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

func TestParseStat(t *testing.T) {
	tests := []struct {
		name  string
		stat  string
		state string
		ppid  string
	}{
		{"plain", "42 (sleep) S 7 42 42 0 -1", "S", "7"},
		{"comm with a paren and spaces", "42 (a) b) c) Z 9 1 1 0", "Z", "9"},
		{"comm with a newline", "42 (x\ny) R 3 1 1 0", "R", "3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := parseStat([]byte(tc.stat))
			if err != nil {
				t.Fatalf("parseStat: %v", err)
			}
			if fields[0] != tc.state || fields[1] != tc.ppid {
				t.Errorf("state, ppid = %q, %q, want %q, %q", fields[0], fields[1], tc.state, tc.ppid)
			}
		})
	}
	for _, bad := range []string{"", "42 sleep S 7", "42 (sleep)", "42 (sleep) S"} {
		if _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("parseStat(%q) succeeded, want an error", bad)
		}
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
	time.Sleep(500 * time.Millisecond)

	sid, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("getsid: %v", err)
	}
	ourSession := strconv.Itoa(sid)
	pids, err := sys.Descendants()
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	var found bool
	for _, pid := range pids {
		cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			continue
		}
		if string(cmdline) == "sleep\x00300\x00" && statFields(t, pid)[3] != ourSession {
			found = true
		}
	}
	if !found {
		t.Fatalf("Descendants = %v, want it to include the orphaned `sleep 300` of another session", pids)
	}
}

func TestLinuxSystemSignalRefusesForeignPid(t *testing.T) {
	sys := newLinuxSystem(t)
	// The parent (go test, or PID 1 in a container) is not a descendant.
	if err := sys.Signal(os.Getppid(), 0); err != nil {
		t.Errorf("Signal(parent, 0) = %v, want nil: a foreign pid is \"not mine\", not an error", err)
	}

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
// the process.
const helperEnv = "PULSIX_SUPERVISOR_TEST_HELPER"

func TestHelperMain(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process for TestMain*; runs only when re-executed")
	}
	i := slices.Index(os.Args, "--")
	os.Exit(Main(os.Args[i+1:]))
}

// runMain runs Main in a child process on the given shell script and
// returns how that process ended.
func runMain(t *testing.T, script string) *os.ProcessState {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperMain$", "--",
		"--grace", "1s", "--", "/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
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
		ps := runMain(t, "exit "+strconv.Itoa(code))
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
		ps := runMain(t, "kill -"+strconv.Itoa(int(sig))+" $$")
		ws, ok := ps.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != sig {
			t.Errorf("runner killed by %v: supervisor ended %v, want death by the same signal", sig, ps)
		}
		if ps.ExitCode() >= 0 {
			t.Errorf("runner killed by %v: ExitCode() = %d, want negative as for a signalled runner", sig, ps.ExitCode())
		}
	}
}
