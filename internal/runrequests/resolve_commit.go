package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
)

// commit is the picture's commit for a run of repo's playbook (spec
// 2026-11-25 A, D3); repo is nil for a template whose playbook comes from
// no repository, which has no commit unless it is a pinned retry:
//   - a retry of a pinned job shows that job's commit (Source
//     CommitFromOriginalRun), which the retry runs again whatever the
//     repository synced since, blocked (BlockedRepoMoved) when the
//     template's playbook now comes from another repository or none, so
//     the approval is refused instead of the worker failing later;
//   - an approved request shows the commit its approval stored, nothing
//     blocking, so a sync after the approval leaves its picture (and the
//     claim guard's fingerprint) as approved; one approved before pins
//     existed shows none, as its approved picture did;
//   - anything else shows the commit of the repository's last successful
//     sync, live, with why it cannot be pinned now: the repository must
//     be synced first (repos.SyncedCommit's Stale) and a sync of it is
//     queued or running (BlockedSyncRunning).
func (r *Resolver) commit(ctx context.Context, orgID uuid.UUID, req Request, repo *repos.Repo) (*PictureCommit, error) {
	// A retry of a pinned job runs that job's commit, which never changes:
	// pending and approved alike, so the claim's picture is the approved
	// one (the approval stored the same commit).
	if req.Kind == KindRetryFailed && req.JobID != nil {
		pin, err := r.src.SyncJobs.PinnedCommit(ctx, orgID, *req.JobID)
		if err != nil && !errors.Is(err, jobs.ErrNotFound) {
			return nil, fmt.Errorf("look up the retried job's commit: %w", err)
		}
		if pin.Commit != "" {
			c := &PictureCommit{SHA: pin.Commit, Source: CommitFromOriginalRun}
			if repo == nil || (pin.RepoID != nil && *pin.RepoID != repo.ID) {
				c.Blocked = []string{BlockedRepoMoved}
			}
			return c, nil
		}
	}
	if repo == nil {
		return nil, nil
	}
	if req.Status == StatusApproved {
		if req.PinnedCommit == "" {
			return nil, nil
		}
		return &PictureCommit{SHA: req.PinnedCommit}, nil
	}
	synced, err := r.src.Commits.SyncedCommit(ctx, orgID, *repo)
	if err != nil {
		return nil, fmt.Errorf("look up the repository's synced commit: %w", err)
	}
	c := &PictureCommit{SHA: synced.Commit}
	if synced.Stale != "" {
		c.Blocked = append(c.Blocked, synced.Stale)
	}
	active, err := r.src.SyncJobs.HasActiveSync(ctx, orgID, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("look up the repository's syncs: %w", err)
	}
	if active {
		c.Blocked = append(c.Blocked, BlockedSyncRunning)
	}
	return c, nil
}
