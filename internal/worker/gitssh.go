package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunHomeDirName is the run's private HOME under its work dir (the runner's
// layout uses the same directory for Ansible's scratch).
const RunHomeDirName = "home"

// RunGitKnownHosts creates the run's ~/.ssh under workDir and returns the
// known_hosts path git's ssh uses there. ssh only remembers a host key when
// the file's directory exists.
func RunGitKnownHosts(workDir string) (string, error) {
	dir := filepath.Join(workDir, RunHomeDirName, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the run's ssh dir: %w", err)
	}
	return filepath.Join(dir, "known_hosts"), nil
}

// GitSSHCommand is the GIT_SSH_COMMAND for a git process the worker starts
// (repo fetches, ansible-galaxy's git sources). OpenSSH resolves ~/.ssh
// from the account's passwd home and ignores $HOME, and that home is
// writable by any playbook running as the worker's user, so nothing there
// is read:
//   - -F /dev/null: no ~/.ssh/config (a planted ProxyCommand would run on
//     every later fetch, final re-review 2) and no system ssh_config;
//   - knownHosts, a file under the run's work dir, replaces the account's
//     known_hosts and the system file is off: host keys are accepted on
//     first use (backlog 2.41 tracks real pinning) and remembered only for
//     that run;
//   - the identity is exactly keyFile, or none at all without one — never
//     the account's default ~/.ssh/id_* files ("IdentityFile none" loads
//     nothing; /dev/null would be tried as a key and warn) — and no agent
//     is asked.
//
// BatchMode keeps ssh from ever prompting.
func GitSSHCommand(keyFile, knownHosts string) string {
	parts := []string{"ssh", "-F", "/dev/null"}
	if keyFile != "" {
		parts = append(parts, "-i", shellQuote(keyFile))
	} else {
		parts = append(parts, "-o", "IdentityFile=none")
	}
	parts = append(parts,
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "UserKnownHostsFile="+shellQuote(knownHosts),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes")
	return strings.Join(parts, " ")
}

// shellQuote quotes s for the shell git runs GIT_SSH_COMMAND through.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
