// The repo-sync executor (backlog 2.11's sync_repo, Task 11): it
// materializes the organization's playbook repository into the worker's
// persistent org-scoped cache (git clone, or fetch+reset when a clone
// already sits there) and installs the roles and collections its
// requirements files name into the repository's per-commit requirements
// cache, the one runs of that commit use (sync_requirements.go, spec
// 2026-11-06-requirements-cache). The manager's claim enrichment
// (internal/jobs) resolved url, branch and the repo's git credential into
// the kind payload and the claim's credentials map.
//
// Nothing here touches a managed host: a broken claim is an executor
// error, a failed fetch or a failed requirements install fails the job
// with the (URL-redacted) reason. A fetched repository's playbook variable
// sources are read next (sync_sources.go), with the roles of the commit's
// requirements entry; nothing in that read fails the job. Success emits
// the requirements step's lines as stdout events, then one run event
// carrying {"repo_id", "requirements_installed", "var_sources"} (the
// sources only as a summary) for the run view.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// kindSyncRepo is this executor's job kind.
const kindSyncRepo = "sync_repo"

// RepoSpec identifies one repository the worker materializes: the fetch
// needs url and branch; subpath is the playbook root inside the clone
// (where the requirements files are looked for first). The shape the runner's
// repo-playbook fetch shares. HostKey/HostPublicKey are the repository's
// git server key pin (backlog 2.41) — the fingerprint plus the full key
// the fetch's own known_hosts is pre-seeded with; empty means unpinned,
// trust on first use.
type RepoSpec struct {
	OrgID         uuid.UUID
	RepoID        uuid.UUID
	URL           string
	Branch        string
	Subpath       string
	HostKey       string
	HostPublicKey string
}

// RepoFetcher materializes a repository into the worker's persistent cache
// and returns the clone's local directory plus — when the fetch spoke ssh
// and the server's key landed in the run's known_hosts — the host key the
// git server presented (nil without one). The runner executor implements
// it — its repo-playbook fetch is the same mechanism. HeadCommit is the
// commit a fetched clone's working tree is at.
type RepoFetcher interface {
	FetchRepoDir(ctx context.Context, spec RepoSpec, secretJSON, cacheRoot, workDir string) (string, *GitHostKey, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
}

// Requirements installs a commit's requirements into the repository's
// per-commit cache and hands out the published entry (spec
// 2026-11-06-requirements-cache); *requirements.Service implements it,
// wired through runner.NewRequirements.
type Requirements interface {
	Ensure(ctx context.Context, req requirements.Request) (requirements.Entry, error)
}

// SyncExecutor executes sync_repo jobs: materialize the repository,
// install the requirements of its commit, then read its playbook variable
// sources.
type SyncExecutor struct {
	// Fetcher materializes the repository; unwired is a wiring bug the
	// executor fails closed on.
	Fetcher RepoFetcher
	// Requirements installs the commit's requirements; the runner's
	// instance (runner.NewRequirements), so a sync and the runs of its
	// commit share one install. Unwired fails a sync whose commit has
	// requirements files closed (the plan's Ruling 12).
	Requirements Requirements
	// Log receives what the read of the variable sources could not do;
	// slog.Default() when nil.
	Log *slog.Logger

	// sourcesTimeLimit bounds the read of the variable sources;
	// varSourcesTimeLimit when zero.
	sourcesTimeLimit time.Duration
	// parseRepo reads the sources with the installed roles;
	// varsource.ParseRepoWithRoles when nil (a seam for the tests).
	parseRepo parseRepoFunc
}

// NewSyncExecutor wires the executor.
func NewSyncExecutor(fetcher RepoFetcher, reqs Requirements) *SyncExecutor {
	return &SyncExecutor{Fetcher: fetcher, Requirements: reqs}
}

// syncPayload is the claim's kind payload: the repository the manager
// resolved at claim time. host_key/host_public_key are the repo's git
// server key pin (backlog 2.41) — empty while the repo is unpinned.
type syncPayload struct {
	Repo *struct {
		ID            string `json:"id"`
		URL           string `json:"url"`
		Branch        string `json:"branch"`
		Subpath       string `json:"subpath"`
		HostKey       string `json:"host_key"`
		HostPublicKey string `json:"host_public_key"`
	} `json:"repo"`
}

// Run materializes the repository, installs the requirements of its
// commit and reads its variable sources.
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
		HostKey: payload.Repo.HostKey, HostPublicKey: payload.Repo.HostPublicKey,
	}
	dir, observed, err := e.Fetcher.FetchRepoDir(ctx, spec, job.GetCredentials()["git:"+repoID.String()], env.RepoCacheDir, env.WorkDir)
	if err != nil {
		// The fetcher's reasons name the command and exit code with URLs
		// redacted; the git key/token files are already wiped.
		return StatusError, nil, fmt.Errorf("fetch repository: %w", err)
	}

	installed, err := e.ensureRequirements(ctx, dir, payload.Repo.Subpath, env, sink)
	if err != nil {
		return StatusError, nil, err
	}
	sources, summary := e.readVarSources(ctx, dir, payload.Repo.Subpath, installed)
	if err := ctx.Err(); err != nil {
		// The job was cancelled during the read: its report is partial.
		return StatusError, nil, fmt.Errorf("read variable sources: %w", err)
	}

	// The one run event the sync reports; its payload carries no
	// credential material. The observed git server key (backlog 2.41) is
	// the manager's TOFU input: an unpinned repo reports what its git
	// server presented (https fetches present none), a pinned one nothing
	// — the fetch already enforced the pin itself, so there is nothing new
	// to record. The values are non-secret host-key material.
	report := map[string]any{"repo_id": payload.Repo.ID, "requirements_installed": !installed.IsZero()}
	if observed != nil && payload.Repo.HostKey == "" {
		report["host_key"] = observed.Fingerprint
		report["host_public_key"] = observed.PublicKey
	}
	if summary != nil {
		report[VarSourcesStatsKey] = summary
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return StatusError, nil, fmt.Errorf("encode sync result: %w", err)
	}
	if err := sink(&workerpb.RunEvent{Task: kindSyncRepo, State: evStateOk, Payload: string(raw)}); err != nil {
		return StatusError, nil, fmt.Errorf("send sync result: %w", err)
	}
	// The stats carry the whole report in place of the event's summary.
	stats := maps.Clone(report)
	if sources != nil {
		stats[VarSourcesStatsKey] = sources
	}
	return StatusSucceeded, stats, nil
}
