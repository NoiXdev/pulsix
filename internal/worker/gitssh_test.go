package worker

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

// TestGitSSHPinnedCommand pins the pinned repo's posture (backlog 2.41):
// the exact accept-new command with StrictHostKeyChecking swapped to yes —
// the run's own known_hosts is pre-seeded with the pin, and strict checking
// demands exactly that key.
func TestGitSSHPinnedCommand(t *testing.T) {
	got := GitSSHPinnedCommand("/run/gitkeys/r", "/run/home/.ssh/known_hosts")
	want := "ssh -F /dev/null -i '/run/gitkeys/r' -o IdentitiesOnly=yes -o IdentityAgent=none" +
		" -o UserKnownHostsFile='/run/home/.ssh/known_hosts' -o GlobalKnownHostsFile=/dev/null" +
		" -o StrictHostKeyChecking=yes -o BatchMode=yes"
	if got != want {
		t.Fatalf("GitSSHPinnedCommand =\n %s\nwant\n %s", got, want)
	}
	// ssh -G accepts it and resolves it to the strict posture (this
	// OpenSSH normalizes "yes" to "true" in the resolution output).
	if _, err := exec.LookPath("ssh"); err == nil {
		cmd := exec.Command("sh", "-c", GitSSHPinnedCommand("", knownHostsForG(t))+" -G git@git.example.org")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -G rejected the pinned command: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "stricthostkeychecking true") {
			t.Fatalf("the pinned command does not resolve strict: %s", out)
		}
	}
}

// knownHostsForG is a plain known_hosts path for the ssh -G resolution.
func knownHostsForG(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "known_hosts")
}

// TestGitKnownHostsName pins the known_hosts name ssh matches a repo's git
// server under — the seed and the capture must agree on it.
func TestGitKnownHostsName(t *testing.T) {
	cases := []struct {
		url     string
		want    string
		wantErr bool
	}{
		{"ssh://git@git.example.com/acme/x.git", "git.example.com", false},
		{"ssh://git@git.example.com:2222/acme/x.git", "[git.example.com]:2222", false},
		{"git@git.example.com:acme/x.git", "git.example.com", false},
		{"git.example.com:acme/x.git", "git.example.com", false},
		{"https://git.example.com/acme/x.git", "", false},
		{"SSH://Git@GIT.example.COM/x.git", "git.example.com", false},
		{"[bad", "", true},
		{"/tmp/fixture", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got, err := GitKnownHostsName(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GitKnownHostsName = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("GitKnownHostsName: %v", err)
			}
			if got != tc.want {
				t.Fatalf("name = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSeedGitHostKey covers the pre-seed: the entry lands 0600, a
// fingerprint mismatch and a multi-line key fail closed, and a non-ssh URL
// is refused (nothing to enforce against).
func TestSeedGitHostKey(t *testing.T) {
	pubKey := gitTestKeyPair(t)
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	fp := gitTestFingerprint(t, pubKey)
	// The known_hosts path the fetch flow hands over: RunGitKnownHosts has
	// already created the run's ssh dir.
	knownHosts, err := RunGitKnownHosts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := SeedGitHostKey(knownHosts, "ssh://git@127.0.0.1:2222/x.git", fp, pub); err != nil {
		t.Fatalf("SeedGitHostKey: %v", err)
	}
	raw, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if fi, statErr := os.Stat(knownHosts); statErr != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("seeded known_hosts mode = %v", fi.Mode())
	}
	if want := "[127.0.0.1]:2222 " + pub; string(raw) != want+"\n" {
		t.Fatalf("seeded line = %q, want %q", raw, want)
	}

	cases := []struct {
		name string
		url  string
		key  string
	}{
		{"fingerprint mismatch", "ssh://git@127.0.0.1:2222/x.git", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(gitTestKeyPair(t))))},
		{"multi-line key", "ssh://git@127.0.0.1:2222/x.git", pub + "\nother-host ssh-ed25519 AAAA"},
		{"non-ssh url", "https://git.example.com/x.git", pub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := SeedGitHostKey(filepath.Join(t.TempDir(), "known_hosts"), tc.url, fp, tc.key); err == nil {
				t.Fatal("an unenforceable pin must fail closed")
			}
		})
	}
}

// TestObservedGitHostKey covers the capture: the entry ssh wrote is
// re-parsed into fingerprint + authorized-key form, and an empty file (an
// https fetch) observes nothing.
func TestObservedGitHostKey(t *testing.T) {
	pubKey := gitTestKeyPair(t)
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	fp := gitTestFingerprint(t, pubKey)
	knownHosts := filepath.Join(t.TempDir(), "home", ".ssh", "known_hosts")

	// Empty (an https fetch never wrote a line).
	if got, err := ObservedGitHostKey(knownHosts, "ssh://git@127.0.0.1:2222/x.git"); err != nil || got != nil {
		t.Fatalf("empty capture = %v, %v", got, err)
	}

	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		t.Fatal(err)
	}
	line := "[127.0.0.1]:2222 " + pub + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ObservedGitHostKey(knownHosts, "ssh://git@127.0.0.1:2222/x.git")
	if err != nil || got == nil {
		t.Fatalf("capture = %v, %v", got, err)
	}
	if got.Fingerprint != fp || got.PublicKey != pub {
		t.Fatalf("capture = %+v", got)
	}
}

// gitTestKeyPair generates one ed25519 host key.
func gitTestKeyPair(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub
}

// gitTestFingerprint is the SHA256 fingerprint of a public key.
func gitTestFingerprint(t *testing.T, pub ssh.PublicKey) string {
	t.Helper()
	return ssh.FingerprintSHA256(pub)
}
