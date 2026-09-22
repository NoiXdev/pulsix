package worker

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
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

// gitSSHCommand composes the GIT_SSH_COMMAND for a git process the worker
// starts (repo fetches, ansible-galaxy's git sources); checking is the
// StrictHostKeyChecking value — accept-new for TOFU, yes for a pinned
// repo's enforced fetch (backlog 2.41). OpenSSH resolves ~/.ssh from the
// account's passwd home and ignores $HOME, and that home is writable by any
// playbook running as the worker's user, so nothing there is read:
//   - -F /dev/null: no ~/.ssh/config (a planted ProxyCommand would run on
//     every later fetch, final re-review 2) and no system ssh_config;
//   - knownHosts, a file under the run's work dir, replaces the account's
//     known_hosts and the system file is off: host keys are accepted on
//     first use and remembered only for that run;
//   - the identity is exactly keyFile, or none at all without one — never
//     the account's default ~/.ssh/id_* files ("IdentityFile none" loads
//     nothing; /dev/null would be tried as a key and warn) — and no agent
//     is asked.
//
// BatchMode keeps ssh from ever prompting.
func gitSSHCommand(keyFile, knownHosts, checking string) string {
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
		"-o", "StrictHostKeyChecking="+checking,
		"-o", "BatchMode=yes")
	return strings.Join(parts, " ")
}

// GitSSHCommand is the GIT_SSH_COMMAND for an unpinned repo's fetch (or a
// git source without a repo pin): host keys are accepted on first use
// (backlog 2.41 tracks real pinning) and remembered only for that run.
func GitSSHCommand(keyFile, knownHosts string) string {
	return gitSSHCommand(keyFile, knownHosts, "accept-new")
}

// GitSSHPinnedCommand is GitSSHCommand for a pinned repository's fetch
// (backlog 2.41): the run's known_hosts was pre-seeded with the pinned key
// (SeedGitHostKey), so strict checking demands exactly that key — a changed
// git server key fails the connection before any data is transferred, and
// its diagnostic names the key the server now presents.
func GitSSHPinnedCommand(keyFile, knownHosts string) string {
	return gitSSHCommand(keyFile, knownHosts, "yes")
}

// GitKnownHostsName is the known_hosts name ssh matches a repo URL's git
// server under: "host" on the default port 22, "[host]:port" for an
// explicit non-default port (ssh://git@host:port/…), and "" when the URL
// carries no ssh host at all (https://… fetches have no host key).
func GitKnownHostsName(repoURL string) (string, error) {
	host, port, ok, err := gitHost(repoURL)
	if err != nil || !ok {
		return "", err
	}
	if port == "22" {
		return strings.ToLower(host), nil
	}
	return "[" + strings.ToLower(host) + "]:" + port, nil
}

// gitHost resolves a repo URL's git server host and port. ssh:// URLs carry
// an optional port in the authority (IPv6 brackets honored; the scheme is
// case-insensitive — git normalizes it); the scp-like form user@host:path
// never carries a port (22). An https:// (or any other scheme'd) URL is not
// an ssh remote: ok is false and there is no host key.
func gitHost(repoURL string) (host, port string, ssh bool, err error) {
	port = "22"
	if len(repoURL) >= 6 && strings.EqualFold(repoURL[:6], "ssh://") {
		rest := repoURL[6:]
		authority, _, _ := strings.Cut(rest, "/")
		// userinfo sits before the last @ (a user name cannot contain @).
		if i := strings.LastIndex(authority, "@"); i >= 0 {
			authority = authority[i+1:]
		}
		if authority == "" {
			return "", "", false, fmt.Errorf("ssh url without a host")
		}
		if strings.HasPrefix(authority, "[") {
			end := strings.Index(authority, "]")
			if end < 0 {
				return "", "", false, fmt.Errorf("unterminated ipv6 literal")
			}
			host = authority[1:end]
			if tail := authority[end+1:]; tail != "" {
				if !strings.HasPrefix(tail, ":") {
					return "", "", false, fmt.Errorf("malformed ssh authority")
				}
				port = strings.TrimPrefix(tail, ":")
			}
			return host, port, true, nil
		}
		if strings.Contains(authority, ":") {
			if h, p, splitErr := net.SplitHostPort(authority); splitErr == nil {
				host, port = h, p
			} else {
				return "", "", false, fmt.Errorf("malformed ssh authority")
			}
		} else {
			host = authority
		}
		return host, port, true, nil
	}
	if strings.Contains(repoURL, "://") {
		// https:// and every other scheme: no ssh remote.
		return "", "", false, nil
	}
	// The scp-like form: user@host:path (the repos registry only accepts
	// https://, ssh:// and this shape).
	scpl, _, ok := strings.Cut(repoURL, ":")
	if !ok || strings.Contains(scpl, "/") {
		return "", "", false, fmt.Errorf("malformed scp-like url")
	}
	if i := strings.LastIndex(scpl, "@"); i >= 0 {
		scpl = scpl[i+1:]
	}
	if scpl == "" {
		return "", "", false, fmt.Errorf("scp-like url without a host")
	}
	return scpl, "22", true, nil
}

// shellQuote quotes s for the shell git runs GIT_SSH_COMMAND through.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// GitHostKey is the git server host key a repo fetch observed (backlog
// 2.41's TOFU capture): the OpenSSH fingerprint and the full key in
// authorized-key form, exactly the pair the sync result reports and the
// manager pins.
type GitHostKey struct {
	Fingerprint string
	PublicKey   string
}

// SeedGitHostKey pre-seeds the run's known_hosts with a pinned repo's key
// (backlog 2.41): the entry the fetch's own ssh connection matches — the
// same name the fetch would write, demanding the pinned key under
// StrictHostKeyChecking=yes. The pin fails closed unless it is one
// authorized-key line whose fingerprint is the claimed fingerprint and the
// URL names an ssh host — an unenforceable pin never connects, the same
// rule pinHosts applies to hosts.
func SeedGitHostKey(knownHostsPath, repoURL, fingerprint, publicKey string) error {
	name, err := GitKnownHostsName(repoURL)
	if err != nil {
		return fmt.Errorf("repo url host: %w", err)
	}
	if name == "" {
		return fmt.Errorf("repo url names no ssh host")
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("the pinned key is not one authorized-key line")
	}
	if fingerprint != "" && ssh.FingerprintSHA256(pub) != fingerprint {
		return fmt.Errorf("the pinned key does not match its fingerprint")
	}
	line := name + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	f, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("seed the run's known_hosts: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("seed the run's known_hosts: %w", err)
	}
	return nil
}

// ObservedGitHostKey reads the key a repo's git server presented during a
// fetch whose ssh wrote knownHostsPath (trust on first use lands there):
// the first entry whose names match the URL's known_hosts name, nil when
// none does — an https fetch leaves the file empty, so a token repo reports
// no host key. The entry ssh itself wrote is re-parsed here, so the capture
// is exactly what OpenSSH verified and recorded.
func ObservedGitHostKey(knownHostsPath, repoURL string) (*GitHostKey, error) {
	name, err := GitKnownHostsName(repoURL)
	if err != nil || name == "" {
		// A URL that is not an ssh remote (or cannot be parsed) has no
		// host key to capture: nothing observed. The capture is
		// best-effort by nature — the fetch itself already succeeded.
		return nil, nil
	}
	raw, err := os.ReadFile(knownHostsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the run's known_hosts: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// The known_hosts line: [marker ]names key-type base64 [comment].
		_, hosts, pub, _, _, err := ssh.ParseKnownHosts([]byte(line))
		if err != nil {
			continue // an unreadable line is never a capture
		}
		for _, h := range hosts {
			if h == name {
				return &GitHostKey{
					Fingerprint: ssh.FingerprintSHA256(pub),
					PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
				}, nil
			}
		}
	}
	return nil, nil
}
