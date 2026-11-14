//go:build !windows

package script

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// processAlive reports whether pid names a live process (or a zombie its
// new parent has not reaped yet).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestExecRunnerKillsAShellScriptsTree: a shell script that starts a
// background child and sleeps is killed with that child on timeout.
func TestExecRunnerKillsAShellScriptsTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "tree.sh")
	body := "sleep 60 &\nprintf %s $! > '" + pidFile + "'\nsleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	const timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	_, err := newExecRunner().Run(ctx, []string{"/bin/sh", script})
	if !errors.Is(err, errTimeout) {
		t.Fatalf("Run error = %v, want errTimeout", err)
	}
	if took := time.Since(start); took > timeout+5*time.Second {
		t.Fatalf("Run took %v", took)
	}
	waitGone(t, readPid(t, pidFile))
}

// TestExecRunnerShellScriptExitCode: a real shell script's exit code and
// output.
func TestExecRunnerShellScriptExitCode(t *testing.T) {
	script := filepath.Join(t.TempDir(), "check.sh")
	body := "echo 'WARNING - disk 81% | used=81%;80;90'\necho noise >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := newExecRunner().Run(context.Background(), []string{"/bin/sh", script})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.exited || res.exitCode != 1 || string(res.stdout) != "WARNING - disk 81% | used=81%;80;90\n" {
		t.Fatalf("result exited %v code %d stdout %q", res.exited, res.exitCode, res.stdout)
	}
}

// TestExecRunnerKilledBySignal: a process ended by a signal has no exit
// code, and that is a result, not an error.
func TestExecRunnerKilledBySignal(t *testing.T) {
	res, err := newExecRunner().Run(context.Background(), []string{"/bin/sh", "-c", "kill -KILL $$"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.exited {
		t.Fatalf("a killed process exited with code %d", res.exitCode)
	}
}

// TestNewRunsAHostCommand: the plugin New returns checks and runs a real
// command end to end.
func TestNewRunsAHostCommand(t *testing.T) {
	sh := rootOwnedShell(t)
	p := New().(*scriptPlugin)
	got := collect(t, p, map[string]any{"commands": []string{"x"}},
		fakeCommands{argv: map[string][]string{"x": {sh, "-c", "echo 'OK - fine | v=3'"}}})
	if len(got) != 5 || got[0] != `x script.status "ok"` || got[1] != `x script.output "OK - fine"` ||
		got[2] != "x script.exit_code 0" || got[4] != "x:v script.value 3" {
		t.Fatalf("observations %q", got)
	}
}

// TestNewRefusesAUserScript: a script the user owns is refused before it
// runs, through the interpreter-argument rule.
func TestNewRefusesAUserScript(t *testing.T) {
	sh := rootOwnedShell(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root: a temp file is root-owned")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "check.sh")
	if err := os.WriteFile(script, []byte("touch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := collect(t, New().(*scriptPlugin), map[string]any{"commands": []string{"x"}},
		fakeCommands{argv: map[string][]string{"x": {sh, script}}})
	want := fmt.Sprintf(`x script.output "%s: owned by uid %d, not by root"`, script, os.Geteuid())
	if len(got) != 2 || got[0] != `x script.status "unknown"` || got[1] != want {
		t.Fatalf("observations %q, want the refusal %s", got, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the refused script ran")
	}
}
