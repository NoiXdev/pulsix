// The requirements install under the play uid, beyond the boundary itself
// (requirements_test.go): a cancel stops galaxy and the git it started
// across the uid boundary, and what a worker umask of 077 does to the
// take-over today.
package uidboundary

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// hangingHTTP is a listener on loopback that accepts every connection and
// never answers: a git source over http that never finishes.
func hangingHTTP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

// playUIDCmdline returns the command line (NULs read as spaces) of a
// process of the play uid that has want in it, or "" without one.
func playUIDCmdline(t *testing.T, want string) string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	uid := strconv.Itoa(playUID)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		cmdline := strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
		if err != nil || !strings.Contains(cmdline, want) {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(status), "\n") {
			if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
				if fields := strings.Fields(rest); len(fields) > 0 && fields[0] == uid {
					return cmdline
				}
				break
			}
		}
	}
	return ""
}

// TestCancellingARequirementsInstallStopsGalaxyAndGit: galaxy runs as the
// play uid under the supervisor, and a role from a git source makes it
// start git, which hangs on a server that never answers. Cancelling the
// run must stop all of it across the uid boundary (stopGroup through the
// helper's --signal, the supervisor ending every descendant): the job is
// reported cancelled, nothing of the play uid is left in /proc within the
// grace plus one second, and the temp entry is gone. The timeout takes the
// same path (requirements' step context ending) but needs the 10-minute
// bound to pass, so it is not repeated here.
func TestCancellingARequirementsInstallStopsGalaxyAndGit(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	t.Cleanup(func() { reapPlayUIDLeftovers(t) })
	const repoID = "01900000-0000-7000-8000-0000000000f4"

	addr := hangingHTTP(t)
	source := gitFixture(t, map[string]string{
		"requirements.yml": "- {name: hang, src: \"git+http://" + addr + "/hang.git\"}\n",
	})
	job := repoJob(t, "01900000-0000-7000-8000-0000000000e4", repoID, source)

	var hung string
	result, logs, cancelledAt := cancelRun(t, workerConfig(t), job, func() bool {
		// galaxy's git clone (or git's http helper) as the play uid.
		hung = playUIDCmdline(t, "http://"+addr+"/hang.git")
		return hung != ""
	})
	t.Logf("cancelled while %q ran as uid %d", hung, playUID)

	if got := result.GetStatus(); got != "cancelled" {
		t.Errorf("status = %q, want cancelled (error: %q):\n%s", got, result.GetError(), logs)
	}
	if !waitUntil(cancelledAt.Add(stopGrace+time.Second), func() bool { return !playUIDEntryExists(t) }) {
		t.Errorf("a /proc entry of uid %d, live or zombie, is left %v after the cancel: galaxy or its git outlived the install\n%s",
			playUID, stopGrace+time.Second, debugPlayUIDProcesses(t))
	}
	entries, temps := cacheNames(t, requirementsDir(repoID))
	if len(entries) != 0 || len(temps) != 0 {
		t.Errorf("requirements cache holds entries %q and temp entries %q after the cancel, want neither", entries, temps)
	}
	if w := requirementsWarnings(logs); len(w) != 0 {
		t.Errorf("the worker logged %d requirements warnings, want none:\n%s", len(w), strings.Join(w, "\n"))
	}
}

// TestRequirementsUnderAWorkerUmaskOf077 pins what a worker started with
// umask 077 gets today (ansible-core 2.21.4): galaxy inherits the umask
// across the setuid helper and the supervisor, nothing sets it, so what
// galaxy creates itself is owner-only to the play uid. The worker, which
// only shares the group, cannot open it, and the take-over refuses the
// install by name — the first path it meets, the role's directory. This is
// the evidence for the backlog item, not the wanted behaviour: once the
// install sets its own umask the run succeeds and this test must flip to
// TestRequirementsInstallUnderThePlayUID's assertions.
func TestRequirementsUnderAWorkerUmaskOf077(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	const repoID = "01900000-0000-7000-8000-0000000000f5"

	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	result, events, logs := runOneJob(t, repoJob(t, "01900000-0000-7000-8000-0000000000e5", repoID, gitFixture(t, installFixture(t))))

	if got := result.GetStatus(); got != "error" {
		t.Errorf("status = %q, want error under umask 077 (error: %q):\n%s", got, result.GetError(), eventDump(events))
	}
	const want = "install requirements: take over the install: roles/from_installed: "
	if msg := result.GetError(); !strings.HasPrefix(msg, want) || !strings.Contains(msg, "permission denied") {
		t.Errorf("error = %q, want it to start with %q and contain %q", msg, want, "permission denied")
	}
	entries, temps := cacheNames(t, requirementsDir(repoID))
	if len(entries) != 0 || len(temps) != 0 {
		t.Errorf("requirements cache holds entries %q and temp entries %q, want neither", entries, temps)
	}
	if w := requirementsWarnings(logs); len(w) != 0 {
		t.Errorf("the worker logged %d requirements warnings, want none:\n%s", len(w), strings.Join(w, "\n"))
	}
}
