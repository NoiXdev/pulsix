// The commit a run can be pinned to (spec 2026-11-25 assistant API gaps,
// A, decision D3): the commit of the repository's last successful sync,
// as its variable sources record keeps it, and whether that commit still
// stands for the repository as it is configured now.
package repos

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// Why a repository's synced commit cannot be pinned (SyncedCommit.Stale).
// Each is cleared by a sync of the repository (by an updated worker, for
// CommitNotReported).
const (
	// CommitNeverSynced: no successful sync recorded a commit.
	CommitNeverSynced = "never_synced"
	// CommitNotReported: the last sync came from a worker that reads and
	// reports nothing (one minor older); the recorded commit is an earlier
	// sync's.
	CommitNotReported = "not_reported"
	// CommitSyncUnrecorded: the sync that read the commit did not record
	// which URL, branch and subpath it fetched (it ran before migration
	// 00123), so whether the repository changed since is unknown.
	CommitSyncUnrecorded = "sync_unrecorded"
	// CommitRepoChanged: the repository's URL, branch or subpath changed
	// after the sync that read the commit.
	CommitRepoChanged = "repo_changed"
)

// SyncedCommit is the commit of a repository's last successful sync and,
// in Stale, why a run cannot be pinned to it ("" when it can).
type SyncedCommit struct {
	Commit string
	Stale  string
}

// SyncedCommit is repo's last successfully synced commit, checked against
// repo as it is now: repo is the caller's read of the repository, so the
// comparison is with the configuration the caller shows.
func (s *Service) SyncedCommit(ctx context.Context, orgID uuid.UUID, repo Repo) (SyncedCommit, error) {
	rec, err := s.store.GetVarSources(ctx, orgID, repo.ID)
	if errors.Is(err, ErrNotFound) {
		return SyncedCommit{Stale: CommitNeverSynced}, nil
	}
	if err != nil {
		return SyncedCommit{}, fmt.Errorf("get the repository's last sync: %w", err)
	}
	out := SyncedCommit{Commit: rec.Commit}
	switch {
	case !rec.WorkerReads:
		out.Stale = CommitNotReported
	case rec.Commit == "":
		out.Stale = CommitNeverSynced
	case rec.SyncedRef == "":
		out.Stale = CommitSyncUnrecorded
	case rec.SyncedRef != jobs.SyncedRef(repo.URL, repo.Branch, repo.Subpath):
		out.Stale = CommitRepoChanged
	}
	return out, nil
}

// LastSynced is the commit and time of repo's last successful sync as its
// variable sources record keeps them; found is false when no sync recorded
// a commit.
func (s *Service) LastSynced(ctx context.Context, orgID, repoID uuid.UUID) (commit string, at time.Time, found bool, err error) {
	rec, err := s.store.GetVarSources(ctx, orgID, repoID)
	if errors.Is(err, ErrNotFound) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("get the repository's last sync: %w", err)
	}
	if rec.Commit == "" {
		return "", time.Time{}, false, nil
	}
	return rec.Commit, rec.SyncedAt, true, nil
}
