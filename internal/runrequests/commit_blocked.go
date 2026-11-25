package runrequests

import (
	"errors"
	"strings"

	"github.com/noixdev/pulsix/internal/repos"
)

// ErrCommitBlocked refuses an approval while its picture's commit cannot
// be pinned (spec 2026-11-25 A, D3). The error returned is a
// *CommitBlockedError, which says why.
var ErrCommitBlocked = errors.New("the commit this run would run cannot be pinned now")

// CommitBlockedError is an approval refused for the reasons in
// PictureCommit.Blocked.
type CommitBlockedError struct {
	Reasons []string
}

// Error says what to do about each reason.
func (e *CommitBlockedError) Error() string {
	parts := make([]string, len(e.Reasons))
	for i, r := range e.Reasons {
		parts[i] = blockedText(r)
	}
	return strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrCommitBlocked) hold.
func (e *CommitBlockedError) Is(target error) bool { return target == ErrCommitBlocked }

func blockedText(reason string) string {
	switch reason {
	case repos.CommitNeverSynced:
		return "the repository has never been synced; sync it, then approve"
	case repos.CommitNotReported:
		return "the repository's last sync did not report its commit (the worker that ran it is out of date); update the worker and sync the repository, then approve"
	case repos.CommitSyncUnrecorded:
		return "the repository's last sync did not record what it fetched; sync it again, then approve"
	case repos.CommitRepoChanged:
		return "the repository's URL, branch or subpath changed after its last sync; sync it, then approve"
	case BlockedSyncRunning:
		return "a sync of the repository is queued or running; approve once it has finished"
	case BlockedRepoMoved:
		return "the run being retried ran a commit of another repository than the template's playbook uses now; reject this retry and ask for a new run"
	}
	return "the run's commit cannot be pinned (" + reason + ")"
}
