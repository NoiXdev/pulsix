package uidboundary

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
// for its parent's wait(2), which no signal can hasten.
func isLiveUnderUID(status, want string) bool {
	uidMatches := false
	for _, line := range strings.Split(status, "\n") {
		if state, ok := strings.CutPrefix(line, "State:"); ok {
			if s := strings.TrimSpace(state); strings.HasPrefix(s, "Z") || strings.HasPrefix(s, "X") {
				return false
			}
		}
		if uid, ok := strings.CutPrefix(line, "Uid:"); ok {
			fields := strings.Fields(uid)
			uidMatches = len(fields) > 0 && fields[0] == want
		}
	}
	return uidMatches
}

// playUIDProcessExists scans /proc for a live process whose REAL uid (see
// isLiveUnderUID) is the play uid. It is read straight from the kernel
// rather than inferred from the playbook's own output, because the
// playbook's output is exactly what a worker that failed to drop privilege
// could still fake.
func playUIDProcessExists(t *testing.T) bool {
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
		if isLiveUnderUID(string(data), want) {
			return true
		}
	}
	return false
}

// debugPlayUIDProcesses is diagnostic only: dumps every /proc entry matching
// the play uid, including its State line, so a lingering ZOMBIE (already
// dead, reaped only by its parent calling wait) can be told apart from a
// process still actually running. Used by reapPlayUIDLeftovers when its
// kill did not take, and by TestCancellingAPlayUnderThePlayUIDStopsIt once
// that test is un-skipped.
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
