// The fetch guards: the pinned git server key, the spec checks, and the
// promises that no error text names the URL and no argument is option-shaped.
// Fixtures (gitOnPATH, gitFixture, ...) live in gitrepo_test.go.
package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestFetchRepoEnforcesPinnedGitServerKey covers the pin mechanics
// (backlog 2.41) over a wrapped git binary: the pinned spec pre-seeds the
// run's known_hosts and composes the strict GIT_SSH_COMMAND (the assertion
// the brief asks for), an unenforceable pin fails closed before git runs,
// and an unpinned fetch keeps today's accept-new posture. The composed
// command and the pre-seed are observable before the connection itself —
// an unreachable ssh URL is enough for the env dump.
func TestFetchRepoEnforcesPinnedGitServerKey(t *testing.T) {
	gitOnPATH(t)
	// A REAL key pair: the seed verifies that the pinned key carries the
	// pinned fingerprint, so a fake fingerprint would fail closed here.
	_, pinnedPub := sshKey(t)
	fp := ssh.FingerprintSHA256(pinnedPub)
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pinnedPub)))

	envDump := filepath.Join(t.TempDir(), "env")
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nenv >> \"" + envDump + "\"\nexec git \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: wrapper}
	// An ssh:// URL that parses (the seed needs its name) but dials
	// nothing: the composed command and the pre-seed are observable before
	// git fails to connect.
	const sshURL = "ssh://git@127.0.0.1:2222/x.git"

	gitSSHCommandLine := func(t *testing.T) string {
		t.Helper()
		envText, err := os.ReadFile(envDump)
		if err != nil {
			t.Fatal(err)
		}
		line := ""
		for _, l := range strings.Split(string(envText), "\n") {
			if strings.HasPrefix(l, "GIT_SSH_COMMAND=") {
				line = l
			}
		}
		return line
	}

	t.Run("pinned fetch composes strict checking and seeds the pin", func(t *testing.T) {
		workDir := t.TempDir()
		spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: sshURL, Branch: "main",
			Subpath: "playbooks/site.yml", HostKey: fp, HostPublicKey: pub}
		if _, err := r.fetchRepo(context.Background(), spec, "", t.TempDir(), workDir); err == nil {
			t.Fatal("the unreachable ssh server must fail the fetch")
		}
		line := gitSSHCommandLine(t)
		if !strings.Contains(line, "-o StrictHostKeyChecking=yes") {
			t.Errorf("GIT_SSH_COMMAND = %q, must demand the pinned key", line)
		}
		if strings.Contains(line, "accept-new") {
			t.Errorf("a pinned fetch must not trust on first use: %q", line)
		}
		// The pre-seed: the entry the fetch's own ssh connection matches
		// (the run's own known_hosts, final re-review out-of-scope 2).
		seeded, err := os.ReadFile(filepath.Join(workDir, homeDirName, ".ssh", "known_hosts"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "[127.0.0.1]:2222 " + pub + "\n"; string(seeded) != want {
			t.Errorf("known_hosts = %q, want the seeded pin %q", seeded, want)
		}
	})
	t.Run("unpinned fetch keeps accept-new", func(t *testing.T) {
		_ = os.Remove(envDump)
		workDir := t.TempDir()
		spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: sshURL, Branch: "main", Subpath: "playbooks/site.yml"}
		if _, err := r.fetchRepo(context.Background(), spec, "", t.TempDir(), workDir); err == nil {
			t.Fatal("the unreachable ssh server must fail the fetch")
		}
		line := gitSSHCommandLine(t)
		if !strings.Contains(line, "accept-new") {
			t.Fatalf("an unpinned fetch must stay accept-new: %q", line)
		}
	})
	t.Run("an unenforceable pin fails closed before git runs", func(t *testing.T) {
		_ = os.Remove(envDump)
		for name, spec := range map[string]gitRepoSpec{
			"multi-line key":  {OrgID: orgID, RepoID: repoID, URL: sshURL, Branch: "main", Subpath: "p", HostKey: fp, HostPublicKey: pub + "\nevil ssh-ed25519 AAAA"},
			"fp key mismatch": {OrgID: orgID, RepoID: repoID, URL: sshURL, Branch: "main", Subpath: "p", HostKey: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", HostPublicKey: pub},
			"non-ssh url":     {OrgID: orgID, RepoID: repoID, URL: "https://git.example.com/x.git", Branch: "main", Subpath: "p", HostKey: fp, HostPublicKey: pub},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := r.fetchRepo(context.Background(), spec, "", t.TempDir(), t.TempDir()); err == nil {
					t.Fatal("an unenforceable pin must fail closed")
				}
				if _, err := os.Stat(envDump); err == nil {
					t.Fatal("git ran with an unenforceable pin")
				}
			})
		}
	})
}

func TestFetchRepoRejectsBadSpecs(t *testing.T) {
	r := &Runner{GitBin: gitOnPATH(t)}
	cacheRoot, workDir := t.TempDir(), t.TempDir()

	// A missing URL.
	_, err := r.fetchRepo(context.Background(), gitRepoSpec{OrgID: orgID, RepoID: repoID, Branch: "main", Subpath: "site.yml"}, "", cacheRoot, workDir)
	if err == nil {
		t.Error("a repo without a URL must fail")
	}

	// A subpath that escapes the clone.
	source := gitFixture(t)
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "../outside.yml"}
	if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir); err == nil {
		t.Error("a traversal subpath must fail")
	}

	// A playbook that is not in the clone.
	spec = gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/missing.yml"}
	if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir); err == nil {
		t.Error("a missing playbook file must fail")
	}
}

func TestFetchRepoErrorTextNeverNamesTheURL(t *testing.T) {
	// git echoes URLs verbatim on failure — an https URL with embedded
	// credentials included. The fake git reproduces exactly that: it
	// writes the URL into its stderr and exits, so the redaction of the
	// stderr tail is genuinely exercised and the failure reason names the
	// command and exit code only.
	const url = "https://user:secret-token@example.invalid/x.git"
	workDir := t.TempDir()
	wrapper := filepath.Join(workDir, "git")
	script := "#!/bin/sh\n" +
		"echo \"fatal: unable to access '" + url + "/': Could not resolve host\" >&2\n" +
		"exit 128\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: wrapper}
	cacheRoot := t.TempDir()
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: url, Branch: "main", Subpath: "site.yml"}
	_, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir)
	if err == nil {
		t.Fatal("an unclonable repo must fail")
	}
	for _, forbidden := range []string{url, "secret-token", "user:secret"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("the error text must not carry the URL or its credentials (%q): %v", forbidden, err)
		}
	}
	if !strings.Contains(err.Error(), "git clone") || !strings.Contains(err.Error(), "exit 128") {
		t.Errorf("the error still names command and exit code: %v", err)
	}
}

// TestFetchRepoNeverPassesOptionShapedArguments (final review triage): a
// URL or branch starting with "-" is refused before git runs, and every
// git invocation separates options from the URL, branch and paths with
// "--", so a stored value can never become a git option.
func TestFetchRepoNeverPassesOptionShapedArguments(t *testing.T) {
	realGit := gitOnPATH(t)
	argsLog := filepath.Join(t.TempDir(), "git-args")
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: wrapper}
	source := gitFixture(t)

	for name, spec := range map[string]gitRepoSpec{
		"url":    {OrgID: orgID, RepoID: repoID, URL: "-oProxyCommand=touch@evil:x", Branch: "main", Subpath: "playbooks/site.yml"},
		"branch": {OrgID: orgID, RepoID: repoID, URL: source, Branch: "--upload-pack=touch", Subpath: "playbooks/site.yml"},
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(argsLog)
			if _, err := r.fetchRepo(context.Background(), spec, "", t.TempDir(), t.TempDir()); err == nil {
				t.Fatal("an option-shaped value must be refused")
			}
			if _, err := os.Stat(argsLog); err == nil {
				t.Fatal("git ran with an option-shaped value")
			}
		})
	}

	// A normal clone and update separate the options from the values.
	_ = os.Remove(argsLog)
	cacheRoot := t.TempDir()
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}
	for i := 0; i < 2; i++ { // clone, then fetch + reset
		if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, t.TempDir()); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "clone") || strings.HasPrefix(line, "fetch") {
			if !strings.Contains(line, " -- ") {
				t.Errorf("git %q runs without a -- separator", line)
			}
		}
	}
}
