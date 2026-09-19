// The repo-sync executor (backlog 2.11's sync_repo, Task 11): it
// materializes the organization's playbook repository into the worker's
// persistent org-scoped cache (git clone, or fetch+reset when a clone
// already sits there) and, when the repository carries a requirements.yml,
// installs the galaxy roles and collections the requirements name. The
// manager's claim enrichment (internal/jobs) resolved url, branch and the
// repo's git credential into the kind payload and the claim's credentials
// map.
//
// Nothing here touches a managed host: a broken claim is an executor
// error, a failed fetch or a failed requirements install fails the job
// with the (URL-redacted) reason. Success emits one run event carrying
// {"repo_id", "requirements_installed"} for the run view.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// kindSyncRepo is this executor's job kind.
const kindSyncRepo = "sync_repo"

// galaxyDirSuffix marks the per-repo galaxy installation path next to the
// clone: <cache>/<org>/<repo>.galaxy — outside the clone's working tree,
// so the next fetch's `reset --hard` and ansible's own role resolution
// never trip over installed roles.
const galaxyDirSuffix = ".galaxy"

// defaultInstallTimeout bounds the requirements install.
const defaultInstallTimeout = 10 * time.Minute

// RepoSpec identifies one repository the worker materializes: the fetch
// needs url and branch; subpath is the playbook root inside the clone
// (where requirements.yml is looked up first). The shape the runner's
// repo-playbook fetch shares.
type RepoSpec struct {
	OrgID   uuid.UUID
	RepoID  uuid.UUID
	URL     string
	Branch  string
	Subpath string
}

// RepoFetcher materializes a repository into the worker's persistent cache
// and returns the clone's local directory. The runner executor implements
// it — its repo-playbook fetch is the same mechanism.
type RepoFetcher interface {
	FetchRepoDir(ctx context.Context, spec RepoSpec, secretJSON, cacheRoot, workDir string) (string, error)
}

// SyncExecutor executes sync_repo jobs: materialize the repository, then
// install its galaxy requirements when it carries a requirements.yml.
type SyncExecutor struct {
	// Fetcher materializes the repository; unwired is a wiring bug the
	// executor fails closed on.
	Fetcher RepoFetcher
	// GalaxyBin is the ansible-galaxy binary; "ansible-galaxy" when
	// empty.
	GalaxyBin string
	// InstallTimeout bounds the requirements install;
	// defaultInstallTimeout when zero.
	InstallTimeout time.Duration
}

// NewSyncExecutor wires the executor.
func NewSyncExecutor(fetcher RepoFetcher, galaxyBin string) *SyncExecutor {
	return &SyncExecutor{Fetcher: fetcher, GalaxyBin: galaxyBin}
}

// syncPayload is the claim's kind payload: the repository the manager
// resolved at claim time.
type syncPayload struct {
	Repo *struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		Branch  string `json:"branch"`
		Subpath string `json:"subpath"`
	} `json:"repo"`
}

// Run materializes the repository and installs its requirements.
func (e *SyncExecutor) Run(ctx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	if job.GetKind() != kindSyncRepo {
		return StatusError, nil, fmt.Errorf("job kind %q has no repo-sync executor", job.GetKind())
	}
	var payload syncPayload
	if err := json.Unmarshal([]byte(job.GetKindPayload()), &payload); err != nil {
		return StatusError, nil, fmt.Errorf("sync-repo kind payload: %w", err)
	}
	if payload.Repo == nil || payload.Repo.ID == "" || payload.Repo.URL == "" || payload.Repo.Branch == "" {
		return StatusError, nil, fmt.Errorf("sync-repo kind payload carries no repository")
	}
	if e.Fetcher == nil {
		return StatusError, nil, errors.New("sync-repo executor: no repo fetcher wired")
	}
	orgID, err := uuid.Parse(job.GetOrgId())
	if err != nil {
		return StatusError, nil, fmt.Errorf("claim org id: %w", err)
	}
	repoID, err := uuid.Parse(payload.Repo.ID)
	if err != nil {
		return StatusError, nil, fmt.Errorf("sync-repo kind payload: %w", err)
	}
	spec := RepoSpec{
		OrgID: orgID, RepoID: repoID,
		URL: payload.Repo.URL, Branch: payload.Repo.Branch, Subpath: payload.Repo.Subpath,
	}
	dir, err := e.Fetcher.FetchRepoDir(ctx, spec, job.GetCredentials()["git:"+repoID.String()], env.RepoCacheDir, env.WorkDir)
	if err != nil {
		// The fetcher's reasons name the command and exit code with URLs
		// redacted; the git key/token files are already wiped.
		return StatusError, nil, fmt.Errorf("fetch repository: %w", err)
	}

	requirements, found := findRequirements(dir, payload.Repo.Subpath)
	requirementsInstalled := false
	if found {
		if err := e.installRequirements(ctx, requirements, dir, env.WorkDir); err != nil {
			return StatusError, nil, fmt.Errorf("install galaxy requirements: %w", err)
		}
		requirementsInstalled = true
	}

	// The one run event the sync reports; its payload carries no
	// credential material.
	report := map[string]any{"repo_id": payload.Repo.ID, "requirements_installed": requirementsInstalled}
	raw, err := json.Marshal(report)
	if err != nil {
		return StatusError, nil, fmt.Errorf("encode sync result: %w", err)
	}
	if err := sink(&workerpb.RunEvent{Task: kindSyncRepo, State: evStateOk, Payload: string(raw)}); err != nil {
		return StatusError, nil, fmt.Errorf("send sync result: %w", err)
	}
	return StatusSucceeded, report, nil
}

// findRequirements locates the repository's requirements.yml: the playbook
// root (the repo's subpath) first, the clone root as the fallback.
func findRequirements(dir, subpath string) (string, bool) {
	for _, rel := range []string{filepath.Join(subpath, "requirements.yml"), "requirements.yml"} {
		if rel == "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	return "", false
}

// installRequirements runs ansible-galaxy install -r requirements.yml -p
// <per-repo galaxy path>, the path sitting next to the clone
// (<cache>/<org>/<repo>.galaxy). Galaxy sources are public by v0.2b
// contract: the repo's git credential does NOT ride along (the runner's
// askpass/key files are wiped when the fetch returns), so a requirement
// that needs a private credential is a sync failure, not a silent leak.
//
// Galaxy's HOME is the run's, and a git source over ssh remembers host keys
// only in the run's known_hosts: ssh ignores $HOME (final re-review
// out-of-scope 2).
func (e *SyncExecutor) installRequirements(ctx context.Context, requirements, cloneDir, workDir string) error {
	galaxy := e.GalaxyBin
	if galaxy == "" {
		galaxy = "ansible-galaxy"
	}
	timeout := e.InstallTimeout
	if timeout <= 0 {
		timeout = defaultInstallTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, galaxy, "install", "-r", requirements, "-p", filepath.Join(cloneDir, "..", filepath.Base(cloneDir)+galaxyDirSuffix))
	cmd.Dir = cloneDir
	knownHosts, err := RunGitKnownHosts(workDir)
	if err != nil {
		return err
	}
	cmd.Env = append(stripPrivateEnv(os.Environ()),
		"HOME="+filepath.Join(workDir, RunHomeDirName),
		"GIT_SSH_COMMAND="+GitSSHCommand("", knownHosts), "GIT_TERMINAL_PROMPT=0")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("ansible-galaxy: exit %d: %s", exit.ExitCode(), truncateTail(stderr.String(), 4<<10))
		}
		return fmt.Errorf("ansible-galaxy: %v", err)
	}
	return nil
}

// truncateTail keeps a diagnostic buffer bounded.
func truncateTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
