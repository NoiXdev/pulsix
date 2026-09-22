// The worker-side git fetch for repo playbooks (backlog 2.11): the runner
// clones or updates the organization's repository itself — the claim's
// KindPayload carries {"repo": {"id","url","branch","subpath"}} (plus the
// git server key pin, backlog 2.41) and the repo's git credential ships in
// the Credentials map under `git:<repo-id>` (the claim-time enrichment
// internal/jobs does at claim time).
//
// Hygiene:
//   - The cache layout is RepoCacheDir/<org-id>/<repo-id> — org-scoped, so
//     one organization's playbooks never serve another's, and the clone is
//     owner-private.
//   - A credential's secret lives only in a 0600 file under the run's work
//     dir (the loop wipes the dir when the run ends): a git_key via
//     GIT_SSH_COMMAND pointing ssh at the key file, a git_token via a
//     GIT_ASKPASS script that cats a token file. A token is never embedded
//     in a URL (that would persist it in the clone's .git/config) and never
//     exported as a plain environment variable.
//   - The failure reason names the command and its exit code only — never
//     the URL (the one place an operator's token could hide) and never the
//     key material.
//
// v0.2b runs one job at a time per worker, so the cache needs no lock; the
// embedded worker of Task 17 uses its own data dir.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/worker"
)

// gitRepoSpec is one repository the run must materialize, straight from the
// claim's kind payload. HostKey/HostPublicKey are the repository's git
// server key pin (backlog 2.41) — empty while unpinned.
type gitRepoSpec struct {
	OrgID         uuid.UUID
	RepoID        uuid.UUID
	URL           string
	Branch        string
	Subpath       string
	HostKey       string
	HostPublicKey string
}

// gitCredential is one claim credential's revealed Secret, parsed for the
// fetch: a git_key's private key or a git_token's token.
type gitCredential struct {
	PrivateKey string `json:"private_key"`
	Token      string `json:"token"`
}

// fetchRepo materializes the repo in the org-scoped cache and returns the
// local playbook file's absolute path. cacheRoot is the worker's persistent
// repo cache; workDir is the run's private work dir (holds the credential's
// temporary 0600 files, wiped by the loop when the run ends). secretJSON is
// the claim's Secret JSON for `git:<repo-id>`; empty means the repo fetches
// without a credential.
func (r *Runner) fetchRepo(ctx context.Context, spec gitRepoSpec, secretJSON, cacheRoot, workDir string) (string, error) {
	sub, err := safeSubpath(spec.Subpath)
	if err != nil {
		return "", fmt.Errorf("repo subpath: %w", err)
	}
	dir, _, err := r.FetchRepoDir(ctx, worker.RepoSpec{
		OrgID: spec.OrgID, RepoID: spec.RepoID, URL: spec.URL, Branch: spec.Branch,
		HostKey: spec.HostKey, HostPublicKey: spec.HostPublicKey,
	}, secretJSON, cacheRoot, workDir)
	if err != nil {
		return "", err
	}
	playbook := filepath.Join(dir, sub)
	fi, statErr := os.Stat(playbook)
	if statErr != nil || fi.IsDir() {
		return "", fmt.Errorf("playbook %s is not a file in the repository cache", spec.Subpath)
	}
	return playbook, nil
}

// FetchRepoDir materializes a repository in the org-scoped cache and
// returns the clone's local directory — plus the git server key the fetch
// observed (nil without one; backlog 2.41's TOFU capture): clone into
// RepoCacheDir/<org-id>/<repo-id> when absent, fetch+reset to the branch
// tip when present. It is the sync_repo executor's fetch too — a repo sync
// materializes the same directory a repo playbook's claim fetches, minus
// the playbook-file resolution the run jobs need. secretJSON is the claim's
// Secret JSON for `git:<repo-id>`; empty means the repo fetches without a
// credential.
//
// The git server key pin (backlog 2.41): a pinned repo's fetch pre-seeds
// the run's known_hosts with the pinned key and connects strict — a
// changed key fails the connection before any data is transferred (the
// diagnostic names the key the server now presents). An unenforceable pin
// (a key that is not one authorized-key line, or a URL that names no ssh
// host) fails closed before git runs. An unpinned repo stays trust on
// first use; the presented key lands in the run's own known_hosts, where
// the capture reads it for the manager's TOFU write.
func (r *Runner) FetchRepoDir(ctx context.Context, spec worker.RepoSpec, secretJSON, cacheRoot, workDir string) (string, *worker.GitHostKey, error) {
	if spec.URL == "" {
		return "", nil, errors.New("repo has no url")
	}
	if spec.Branch == "" {
		return "", nil, errors.New("repo has no branch")
	}
	// A value starting with "-" would reach git's argument parser as an
	// option; the manager refuses such values, the worker defends again.
	if strings.HasPrefix(spec.URL, "-") || strings.HasPrefix(spec.Branch, "-") {
		return "", nil, errors.New(`repo url or branch must not start with "-"`)
	}

	var cred gitCredential
	if secretJSON != "" {
		if err := json.Unmarshal([]byte(secretJSON), &cred); err != nil {
			return "", nil, fmt.Errorf("git credential: %w", err)
		}
	}

	gitBin := r.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	env := append([]string{}, stripPrivateEnv(os.Environ())...)
	env = append(env, "HOME="+filepath.Join(workDir, homeDirName))
	// ssh ignores $HOME and resolves ~/.ssh from the account's passwd
	// entry, so the run's known_hosts is named explicitly (final re-review
	// out-of-scope 2).
	knownHosts, err := worker.RunGitKnownHosts(workDir)
	if err != nil {
		return "", nil, err
	}
	pinned := spec.HostPublicKey != ""
	if pinned {
		if err := worker.SeedGitHostKey(knownHosts, spec.URL, spec.HostKey, spec.HostPublicKey); err != nil {
			return "", nil, fmt.Errorf("repo %s: the git server pin is unenforceable: %w", spec.RepoID, err)
		}
	}

	// Credential plumbing: the secret reaches git only through a file path.
	gitSSH := worker.GitSSHCommand
	if pinned {
		gitSSH = worker.GitSSHPinnedCommand
	}
	switch {
	case cred.PrivateKey != "":
		keyFile := filepath.Join(workDir, gitKeysDir, spec.RepoID.String())
		if err := writeSecretFile(keyFile, cred.PrivateKey, 0o600); err != nil {
			return "", nil, err
		}
		defer func() { _ = os.Remove(keyFile) }()
		env = append(env, "GIT_SSH_COMMAND="+gitSSH(keyFile, knownHosts), "GIT_TERMINAL_PROMPT=0")
	case cred.Token != "":
		tokenFile := filepath.Join(workDir, gitTokensDir, spec.RepoID.String())
		if err := writeSecretFile(tokenFile, cred.Token, 0o600); err != nil {
			return "", nil, err
		}
		askpass, err := writeAskpass(workDir, spec.RepoID.String(), tokenFile)
		if err != nil {
			return "", nil, err
		}
		// Both files are wiped when the fetch ends.
		defer func() {
			_ = os.Remove(tokenFile)
			_ = os.Remove(askpass)
		}()
		// The askpass answers git's username and password prompts with the
		// token: providers accept a token as the username for clone/fetch,
		// and the secret itself never leaves the token file — the script
		// only cats it.
		env = append(env, "GIT_ASKPASS="+askpass, "GIT_TERMINAL_PROMPT=0")
	}
	if cred.PrivateKey == "" {
		// No key: an ssh:// or scp-like remote still reads and writes
		// host keys only in the run's known_hosts.
		env = append(env, "GIT_SSH_COMMAND="+gitSSH("", knownHosts))
	}

	if err := r.fetchGit(ctx, gitBin, spec, env, cacheRoot, pinned); err != nil {
		return "", nil, err
	}
	if pinned {
		// The pin was enforced by the connection itself: what the server
		// presented equals the pin (the worker reports nothing new — the
		// manager's pin stands).
		return r.repoDir(cacheRoot, spec), nil, nil
	}
	observed, err := worker.ObservedGitHostKey(knownHosts, spec.URL)
	if err != nil {
		return "", nil, fmt.Errorf("observe the git server key: %w", err)
	}
	return r.repoDir(cacheRoot, spec), observed, nil
}

// repoDir is the clone's local directory, the fetch's layout.
func (r *Runner) repoDir(cacheRoot string, spec worker.RepoSpec) string {
	return filepath.Join(cacheRoot, spec.OrgID.String(), spec.RepoID.String())
}

// fetchGit runs the clone-or-update behind FetchRepoDir: fetch+reset into
// an existing clone, a fresh clone otherwise (with a leftover that is not a
// verifiable clone re-cloned rather than built on). A pinned fetch's
// failure is wrapped with the repository it refused — the error names the
// repo, and the redacted stderr tail carries the key the server now
// presented (backlog 2.41's changed-key message).
func (r *Runner) fetchGit(ctx context.Context, gitBin string, spec worker.RepoSpec, env []string, cacheRoot string, pinned bool) error {
	target := r.repoDir(cacheRoot, spec)
	wrap := func(err error) error {
		if !pinned {
			return err
		}
		return fmt.Errorf("repo %s: %w", spec.RepoID, err)
	}
	if _, statErr := os.Stat(filepath.Join(target, ".git")); statErr == nil {
		// Present: bring it to the branch's tip — the run must see the
		// branch's current state, not a stale cache.
		if err := runGit(ctx, gitBin, target, env, spec.URL, "fetch", "--quiet", "--", "origin", spec.Branch); err != nil {
			return wrap(err)
		}
		if err := runGit(ctx, gitBin, target, env, spec.URL, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return wrap(err)
		}
		return nil
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return fmt.Errorf("create repo cache root: %w", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		// A leftover that is not a verifiable clone (a killed earlier
		// run's partial copy): re-clone rather than build on it.
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("clear stale cache dir: %w", err)
		}
	}
	if err := runGit(ctx, gitBin, cacheRoot, env, spec.URL,
		"clone", "--quiet", "--branch="+spec.Branch, "--single-branch", "--", spec.URL, target); err != nil {
		_ = os.RemoveAll(target) // a partial clone must not linger
		return wrap(err)
	}
	_ = os.Chmod(target, 0o700)
	return nil
}

// writeAskpass writes the GIT_ASKPASS script: secret-free (it cats the
// token file), executable, owner-private.
func writeAskpass(workDir, repoID, tokenFile string) (string, error) {
	script := "#!/bin/sh\ncat '" + tokenFile + "'\n"
	path := filepath.Join(workDir, "gitaskpass-"+repoID+".sh")
	if err := writeSecretFile(path, script, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// safeSubpath validates the playbook's subpath inside a clone: non-empty,
// no traversal, no absolute path, no backslashes.
func safeSubpath(sub string) (string, error) {
	if sub == "" {
		return "", errors.New("is empty")
	}
	if strings.HasPrefix(sub, "/") || strings.Contains(sub, `\`) || strings.Contains(sub, "..") {
		return "", errors.New("must be a relative path without traversal")
	}
	return filepath.FromSlash(sub), nil
}

// urlCredPattern matches `user:password@` userinfo inside a URL-shaped
// token (https://, ssh://, and the scp-like form alike) — the password half
// is what must never survive into a job's error text. A user without a
// password (git@github.com) is left alone.
var urlCredPattern = regexp.MustCompile(`[^@/\s:]+:[^@/\s]+@`)

// redactURLs strips credential-bearing URLs from a git diagnostic: every
// occurrence of the repo's own URL goes first (it is the claim's value,
// embeddable credentials included), then any remaining scheme://user:pass@
// userinfo pattern loses its secret half. Over-redaction is acceptable —
// a diagnostic is never worth a secret.
func redactURLs(s, url string) string {
	if url != "" {
		s = strings.ReplaceAll(s, url, "[repo url]")
	}
	return urlCredPattern.ReplaceAllString(s, "[redacted]@")
}

// runGit runs one git command inside dir; its failure names the command and
// exit code, and a bounded stderr tail with every URL credential redacted —
// git echoes the repo URL verbatim on access failures, so the tail must
// never carry it (it can hold an operator's embedded credentials).
func runGit(ctx context.Context, gitBin, dir string, env []string, redactURL string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("git %s: exit %d: %s", args[0], exit.ExitCode(), gitDiagnostic(stderr.String(), redactURL))
		}
		return fmt.Errorf("git %s: %v", args[0], err)
	}
	return nil
}

// gitDiagnostic is a git failure's bounded stderr tail (4 KiB), redacted
// FIRST: truncating before redacting can cut a URL's "user:token@" so the
// userinfo pattern no longer matches and the token's tail survives.
func gitDiagnostic(stderr, url string) string {
	return truncate(redactURLs(stderr, url), 4<<10)
}

// truncate keeps a diagnostic buffer bounded.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
