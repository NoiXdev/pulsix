package playbooks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
)

var (
	// ErrNotRepo: the operation is for a repo playbook only; an editor
	// playbook's text is its inline document.
	ErrNotRepo = errors.New("not a repo playbook")
	// ErrNoRepoText: no sync recorded the playbook's repository.
	ErrNoRepoText = errors.New("the repository has not been synced yet")
)

// TextNotSent is RepoText's LeftOut when the last sync stored no text for
// the playbook and named no reason: a worker older than the texts, a row
// kept from before them, or a path the last sync did not report (the file
// was removed). The other reasons are varsource's Text*.
const TextNotSent = "not_sent"

// RepoText is a repo playbook's text as of its repository's last sync
// (backlog 3.127). Content is nil when the sync stored none; LeftOut then
// says why. Commit is empty when the last sync left the playbook unread
// (a cap) and the text is from an earlier read.
type RepoText struct {
	Path     string
	Commit   string
	SyncedAt time.Time
	Content  *string
	LeftOut  string
}

// RepoText is the stored text of a repo playbook: ErrNotFound when the
// playbook does not exist in the organization, ErrNotRepo for an editor
// playbook, ErrNoRepoText when no sync recorded its repository.
func (s *Service) RepoText(ctx context.Context, orgID, playbookID uuid.UUID) (RepoText, error) {
	p, err := s.store.Get(ctx, orgID, playbookID)
	if err != nil {
		return RepoText{}, err
	}
	if p.Source != SourceRepo || p.RepoID == nil || p.Path == nil {
		return RepoText{}, fmt.Errorf("%w: an editor playbook's text is its inline document", ErrNotRepo)
	}
	stored, err := s.repos.PlaybookText(ctx, orgID, *p.RepoID, *p.Path)
	if errors.Is(err, repos.ErrNotFound) {
		return RepoText{}, ErrNoRepoText
	}
	if err != nil {
		return RepoText{}, fmt.Errorf("read the playbook's text: %w", err)
	}
	out := RepoText{
		Path: *p.Path, Commit: stored.Commit, SyncedAt: stored.SyncedAt,
		Content: stored.Content, LeftOut: stored.LeftOut,
	}
	if stored.Kept {
		out.Commit = ""
	}
	if out.Content == nil && out.LeftOut == "" {
		out.LeftOut = TextNotSent
	}
	return out, nil
}
