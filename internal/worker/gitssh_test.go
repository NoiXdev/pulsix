package worker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitSSHCommand(t *testing.T) {
	got := GitSSHCommand("/run/gitkeys/r", "/run/home/.ssh/known_hosts")
	want := "ssh -i '/run/gitkeys/r' -o IdentitiesOnly=yes -o UserKnownHostsFile='/run/home/.ssh/known_hosts'" +
		" -o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
	if got != want {
		t.Fatalf("GitSSHCommand =\n %s\nwant\n %s", got, want)
	}
	if got := GitSSHCommand("", "/k"); strings.Contains(got, "-i ") || strings.Contains(got, "IdentitiesOnly") {
		t.Fatalf("no key file must mean no identity options: %s", got)
	}
}

// TestGitSSHCommandSurvivesTheShell: git runs GIT_SSH_COMMAND through sh,
// so a path with spaces or quotes must reach ssh as one argument.
func TestGitSSHCommandSurvivesTheShell(t *testing.T) {
	odd := filepath.Join(t.TempDir(), "a b'c", "known_hosts")
	// Let the shell word-split the command exactly as git does, then print
	// every word on its own line.
	cmd := exec.Command("sh", "-c", "set -- "+GitSSHCommand("", odd)+`; printf '%s\n' "$@"`)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\nUserKnownHostsFile="+odd+"\n") {
		t.Fatalf("the known_hosts path did not survive as one word:\n%s", out)
	}
}

func TestRunGitKnownHostsCreatesTheRunSSHDir(t *testing.T) {
	workDir := t.TempDir()
	path, err := RunGitKnownHosts(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(workDir, "home", ".ssh", "known_hosts") {
		t.Fatalf("path = %s", path)
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("ssh dir = %v, %v; want 0700", fi, err)
	}
}
