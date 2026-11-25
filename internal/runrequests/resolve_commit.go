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
// 2026-11-25 A, D3):
//   - an approved request shows the commit its approval stored, nothing
//     blocking, so a sync after the approval leaves its picture (and the
//     claim guard's fingerprint) as approved; one approved before pins
//     existed shows none, as its approved picture did;
//   - a retry of a pinned job shows that job's commit, which the retry
//     runs again whatever the repository synced since;
//   - anything else shows the commit of the repository's last successful
//     sync, live, with why it cannot be pinned now: the repository must
//     be synced first (repos.SyncedCommit's Stale) and a sync of it is
//     queued or running (BlockedSyncRunning).
func (r *Resolver) commit(ctx context.Context, orgID uuid.UUID, req Request, repo repos.Repo) (*PictureCommit, error) {
	if req.Status == StatusApproved {
		if req.PinnedCommit == "" {
			return nil, nil
		}
		return &PictureCommit{SHA: req.PinnedCommit}, nil
	}
	if req.Kind == KindRetryFailed && req.JobID != nil {
		pinned, err := r.src.SyncJobs.PinnedCommit(ctx, orgID, *req.JobID)
		if err != nil && !errors.Is(err, jobs.ErrNotFound) {
			return nil, fmt.Errorf("look up the retried job's commit: %w", err)
		}
		if pinned != "" {
			return &PictureCommit{SHA: pinned}, nil
		}
	}
	synced, err := r.src.Commits.SyncedCommit(ctx, orgID, repo)
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
