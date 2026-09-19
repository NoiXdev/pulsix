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
	want := "ssh -F /dev/null -i '/run/gitkeys/r' -o IdentitiesOnly=yes -o IdentityAgent=none" +
		" -o UserKnownHostsFile='/run/home/.ssh/known_hosts' -o GlobalKnownHostsFile=/dev/null" +
		" -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
	if got != want {
		t.Fatalf("GitSSHCommand =\n %s\nwant\n %s", got, want)
	}
	// Without a key no identity is offered at all: not the account's
	// default ~/.ssh/id_* files (a playbook could plant one) and no agent.
	want = "ssh -F /dev/null -o IdentityFile=none -o IdentitiesOnly=yes -o IdentityAgent=none" +
		" -o UserKnownHostsFile='/k' -o GlobalKnownHostsFile=/dev/null" +
		" -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
	if got := GitSSHCommand("", "/k"); got != want {
		t.Fatalf("GitSSHCommand without a key =\n %s\nwant\n %s", got, want)
	}
}

// TestGitSSHCommandReadsNoSSHConfig is final re-review 2, minor 2: ssh
// reads ~/.ssh/config from the account's passwd home, which a playbook
// running as the worker's user can write (a ProxyCommand there would run
// on every later fetch). The real ssh client must accept the command and
// resolve it to no config file, only the run's known_hosts and no
// identity beyond the key.
func TestGitSSHCommandReadsNoSSHConfig(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("no ssh client: %v", err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	keyFile := filepath.Join(t.TempDir(), "key") // ssh -G drops an -i file that does not exist
	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", keyFile} {
		// ssh -G prints the resolved configuration without connecting.
		cmd := exec.Command("sh", "-c", GitSSHCommand(key, knownHosts)+" -G git@git.example.org")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -G rejected the command (key %q): %v\n%s", key, err, out)
		}
		cfg := map[string][]string{}
		for _, l := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(l, " "); ok {
				cfg[k] = append(cfg[k], v)
			}
		}
		wantID := "none"
		if key != "" {
			wantID = key
		}
		checks := map[string]string{
			"userknownhostsfile":    knownHosts,
			"globalknownhostsfile":  "/dev/null",
			"identityfile":          wantID,
			"identitiesonly":        "yes",
			"identityagent":         "none",
			"batchmode":             "yes",
			"stricthostkeychecking": "accept-new",
		}
		for k, want := range checks {
			if got := strings.Join(cfg[k], ","); got != want {
				t.Errorf("key %q: %s = %q, want %q", key, k, got, want)
			}
		}
		if len(cfg["proxycommand"]) != 0 || len(cfg["proxyjump"]) != 0 {
			t.Errorf("key %q: a proxy leaked in from an ssh config: %v %v", key, cfg["proxycommand"], cfg["proxyjump"])
		}
	}
	if !strings.Contains(GitSSHCommand("", knownHosts), "-F /dev/null") {
		t.Fatal("the command must not read any ssh config file")
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
