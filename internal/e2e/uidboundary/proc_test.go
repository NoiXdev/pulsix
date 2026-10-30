package uidboundary

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// TestIsLiveUnderUID pins what counts as a play-uid process still running.
// It runs outside the image too: it reads a status text, not /proc.
//
// A zombie is the case that matters. In the image this binary is PID 1, so
// every play process a cancel or a reap orphans is reparented to it and
// stays a zombie for the container's life — nothing here calls wait(2) on
// a pid it did not start. A zombie keeps its Uid: line, so counting it
// made reapPlayUIDLeftovers report survivors after a KILL that had worked,
// and would have let TestCancellingAPlayUnderThePlayUIDStopsIt's "the play
// has started" wait pass on the previous test's corpses.
func TestIsLiveUnderUID(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{"running under the uid", "Name:\tsleep\nState:\tS (sleeping)\nUid:\t65533\t65533\t65533\t65533\n", true},
		{"running, real uid differs", "Name:\tsleep\nState:\tS (sleeping)\nUid:\t65532\t65533\t65533\t65533\n", false},
		{"zombie under the uid", "Name:\tsleep\nState:\tZ (zombie)\nUid:\t65533\t65533\t65533\t65533\n", false},
		{"exiting under the uid", "Name:\tsleep\nState:\tX (dead)\nUid:\t65533\t65533\t65533\t65533\n", false},
		{"zombie leader whose other threads run on", "Name:\tpython3\nState:\tZ (zombie)\nUid:\t65533\t65533\t65533\t65533\nThreads:\t2\n", true},
		{"zombie leader with no thread left", "Name:\tpython3\nState:\tZ (zombie)\nUid:\t65533\t65533\t65533\t65533\nThreads:\t1\n", false},
		{"stopped under the uid", "Name:\tsleep\nState:\tT (stopped)\nUid:\t65533\t65533\t65533\t65533\n", true},
		{"no Uid line", "Name:\tsleep\nState:\tS (sleeping)\n", false},
		{"no State line", "Name:\tsleep\nUid:\t65533\t65533\t65533\t65533\n", true},
		{"uid is a prefix of the real one", "Name:\tsleep\nState:\tS (sleeping)\nUid:\t655330\t655330\t655330\t655330\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLiveUnderUID(tt.status, "65533"); got != tt.want {
				t.Errorf("isLiveUnderUID(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

// TestIsSupervisorCmdline pins which processes playUIDProcessExists leaves
// out of "the play has started". It runs outside the image too: it reads a
// cmdline, not /proc.
func TestIsSupervisorCmdline(t *testing.T) {
	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"the supervisor", "/uid-boundary.test\x00" + supervisor.Arg + "\x00--grace\x005s\x00--\x00/usr/bin/ansible-runner\x00run\x00", true},
		{"the helper about to exec it", "/usr/local/bin/pulsix-setuid-exec\x0065533\x00/uid-boundary.test\x00" + supervisor.Arg + "\x00--grace\x005s\x00", true},
		{"ansible-runner", "/usr/bin/python3\x00/usr/bin/ansible-runner\x00run\x00/var/lib/pulsix-worker/job-1\x00", false},
		{"the play's sleep", "sleep\x00300\x00", false},
		{"the reserved word as argv[0] only", supervisor.Arg + "\x00", false},
		{"the reserved word inside an argument", "sh\x00-c\x00echo " + supervisor.Arg + "\x00", false},
		{"empty, a zombie", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSupervisorCmdline([]byte(tt.cmdline)); got != tt.want {
				t.Errorf("isSupervisorCmdline(%q) = %v, want %v", tt.cmdline, got, tt.want)
			}
		})
	}
}

// playUIDProcessGroups lists the distinct process groups of every live
// process whose real uid is the play uid. The pgrp is field 5 of
// /proc/<pid>/stat, read from the LAST ')' forward because field 2 is the
// comm and a comm may contain spaces and parentheses.
func playUIDProcessGroups(t *testing.T) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	want := strconv.Itoa(playUID)
	seen := map[int]bool{}
	var out []int
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue // exited between ReadDir and ReadFile
		}
		if !isLiveUnderUID(string(status), want) {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		closeParen := strings.LastIndex(string(stat), ")")
		if closeParen < 0 {
			continue
		}
		fields := strings.Fields(string(stat)[closeParen+1:]) // state, ppid, pgrp, ...
		if len(fields) < 3 {
			continue
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil || seen[pgid] {
			continue
		}
		seen[pgid] = true
		out = append(out, pgid)
	}
	return out
}

// isLiveUnderUID reports whether a /proc/<pid>/status text describes a
// process that is still running and whose REAL uid (the first of the Uid:
// line's real, effective, saved, fs) is want. A zombie (Z) or a process
// being torn down (X) is not running: it has already exited and only waits
// for its parent's wait(2), which no signal can hasten. The exception is a
// thread-group leader whose main thread exited while other threads run on:
// it shows as Z too, but its Threads: line counts those threads, and the
// group is alive until they end.
func isLiveUnderUID(status, want string) bool {
	uidMatches, exited, threads := false, false, 0
	for _, line := range strings.Split(status, "\n") {
		if state, ok := strings.CutPrefix(line, "State:"); ok {
			s := strings.TrimSpace(state)
			exited = strings.HasPrefix(s, "Z") || strings.HasPrefix(s, "X")
		}
		if n, ok := strings.CutPrefix(line, "Threads:"); ok {
			threads, _ = strconv.Atoi(strings.TrimSpace(n))
		}
		if uid, ok := strings.CutPrefix(line, "Uid:"); ok {
			fields := strings.Fields(uid)
			uidMatches = len(fields) > 0 && fields[0] == want
		}
	}
	return uidMatches && (!exited || threads > 1)
}

// playUIDProcessExists scans /proc for a live process whose REAL uid (see
// isLiveUnderUID) is the play uid and that is not the play supervisor (see
// isSupervisorCmdline). It is read straight from the kernel rather than
// inferred from the playbook's own output, because the playbook's output is
// exactly what a worker that failed to drop privilege could still fake.
//
// The supervisor is left out because the tests use this as their "the play
// has started" signal. Under the supervisor the first process of the play
// uid is the supervisor itself — the setuid helper execs it in the
// runner's place — and a cancel sent the moment it appears lands before
// ansible-runner, let alone the play, exists: the cancel tests would then
// prove only that a supervisor with nothing below it stops. Counting from
// the first process that is not the supervisor makes the wait release on
// ansible-runner, the supervisor's child: the run has started, but the
// play's own processes may not exist yet. A test that must cancel a play
// that is already running waits for the play's own process instead.
//
// What a test asserts is GONE after a run must not skip anything, which is
// why reapPlayUIDLeftovers uses livePlayUIDProcessExists and
// TestACancelledPlayLeavesNoZombie uses playUIDEntryExists. A supervisor
// still alive after its run has been reported is a survivor like any other.
func playUIDProcessExists(t *testing.T) bool {
	t.Helper()
	return scanLivePlayUID(t, false)
}

// livePlayUIDProcessExists is playUIDProcessExists counting the supervisor
// too: any live process of the play uid at all.
func livePlayUIDProcessExists(t *testing.T) bool {
	t.Helper()
	return scanLivePlayUID(t, true)
}

func scanLivePlayUID(t *testing.T, countSupervisor bool) bool {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	want := strconv.Itoa(playUID)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // not a pid directory
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue // the process exited between ReadDir and ReadFile
		}
		if !isLiveUnderUID(string(data), want) {
			continue
		}
		if !countSupervisor {
			cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
			if err != nil {
				continue // it exited between the two reads
			}
			if isSupervisorCmdline(cmdline) {
				continue
			}
		}
		return true
	}
	return false
}

// isSupervisorCmdline reports whether a raw /proc/<pid>/cmdline (NUL-
// separated) belongs to the play supervisor: one of its arguments after
// argv[0] is supervisor.Arg. For the supervisor itself that is argv[1]
// (`<binary> __pulsix-play-supervisor --grace …`). It is also argv[3] of
// the setuid helper that is about to exec it
// (`<helper> <uid> <binary> __pulsix-play-supervisor …`), which runs as the
// play uid for the moment between its uid drop and its execv; that is the
// supervisor-to-be, not the play, and a poll that caught it must not count
// it either. cmdline rather than /proc/<pid>/exe because exe is a symlink
// the kernel lets only the owner (or a ptrace-capable process) resolve, and
// the supervisor runs as the play uid while this binary runs as the worker
// uid; cmdline is world-readable.
func isSupervisorCmdline(cmdline []byte) bool {
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	for _, a := range args[min(1, len(args)):] {
		if a == supervisor.Arg {
			return true
		}
	}
	return false
}

// debugPlayUIDProcesses is diagnostic only: dumps every /proc entry matching
// the play uid, including its State line, so a lingering ZOMBIE (already
// dead, reaped only by its parent calling wait) can be told apart from a
// process still actually running. Used by reapPlayUIDLeftovers when it
// finds a survivor or its kill did not take, and by the tests that assert
// nothing of a play is left after its run.
func debugPlayUIDProcesses(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Sprintf("read /proc: %v", err)
	}
	want := strconv.Itoa(playUID)
	var b strings.Builder
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		var uidLine, stateLine, nameLine, ppidLine string
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "Uid:"):
				uidLine = l
			case strings.HasPrefix(l, "State:"):
				stateLine = l
			case strings.HasPrefix(l, "Name:"):
				nameLine = l
			case strings.HasPrefix(l, "PPid:"):
				ppidLine = l
			}
		}
		fields := strings.Fields(strings.TrimPrefix(uidLine, "Uid:"))
		if len(fields) == 0 || fields[0] != want {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		fmt.Fprintf(&b, "pid=%s %s %s %s cmdline=%q\n", e.Name(), nameLine, stateLine, ppidLine, strings.ReplaceAll(string(cmdline), "\x00", " "))
	}
	if b.Len() == 0 {
		return "(no matching /proc entries at dump time)"
	}
	return b.String()
}

// playUIDEntryExists reports whether ANY /proc entry belongs to the play
// uid, zombies and dying processes included — unlike playUIDProcessExists,
// which counts only live ones. Under the play supervisor nothing of a
// finished run may be left at all: the supervisor is a child subreaper and
// reaps every descendant before it exits, so a play-uid zombie here means
// something escaped to this binary (PID 1, which never reaps a pid it did
// not start) and is a regression, not noise to filter out.
func playUIDEntryExists(t *testing.T) bool {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	want := strconv.Itoa(playUID)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue // reaped between ReadDir and ReadFile
		}
		for _, line := range strings.Split(string(data), "\n") {
			if uid, ok := strings.CutPrefix(line, "Uid:"); ok {
				if fields := strings.Fields(uid); len(fields) > 0 && fields[0] == want {
					return true
				}
				break
			}
		}
	}
	return false
}

// processWithCmdline finds a process whose /proc/<pid>/cmdline, its NULs
// joined by spaces and trimmed, is exactly want, and returns its pid and
// the state letter of its State: line ("S", "R", "Z", ...).
//
// It cannot see a zombie: the kernel releases a process's memory when it
// exits, and cmdline is read from that memory, so a zombie's cmdline is
// empty. A caller that must also rule out a zombie keeps the pid this
// returned while the process was live and checks processGone on it.
func processWithCmdline(t *testing.T, want string) (pid int, state string, ok bool) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " ")) != want {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue // it ended between the two reads
		}
		return n, procState(string(status)), true
	}
	return 0, "", false
}

// processGone reports whether pid has no /proc entry at all: it has ended
// AND been reaped, so it is neither running nor a zombie. Pids in the
// container's own namespace count up from 1 towards pid_max before they
// wrap, so within one test a pid is not handed to a new process again.
func processGone(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return errors.Is(err, fs.ErrNotExist)
}

// procState is the state letter of a /proc/<pid>/status text's State: line,
// or "" without one.
func procState(status string) string {
	for _, line := range strings.Split(status, "\n") {
		if state, ok := strings.CutPrefix(line, "State:"); ok {
			if fields := strings.Fields(state); len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}
