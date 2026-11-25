package runrequests

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// RepoSyncer starts a repository's sync, or finds the one queued or
// running (repos.Service.Sync, deduplicated per spec 2026-11-25 G).
type RepoSyncer interface {
	Sync(ctx context.Context, orgID, repoID uuid.UUID, actor *uuid.UUID) (job jobs.Job, alreadyRunning bool, err error)
}

// SyncFirst implements Syncer: the sync of the repository the template's
// playbook comes from, started as actor through the repository's own sync
// path; a sync of it already queued or running is the one the request
// waits for. A playbook from no repository (an editor playbook, or none)
// has nothing to sync: nil.
func (r *Resolver) SyncFirst(ctx context.Context, orgID, templateID, actor uuid.UUID) (*uuid.UUID, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	if tmpl.PlaybookID == uuid.Nil {
		return nil, nil
	}
	pb, err := r.src.Playbooks.Get(ctx, orgID, tmpl.PlaybookID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	if pb.RepoID == nil {
		return nil, nil
	}
	job, _, err := r.src.RepoSyncs.Sync(ctx, orgID, *pb.RepoID, &actor)
	if err != nil {
		return nil, mapNotFound(fmt.Errorf("sync the repository first: %w", err))
	}
	return &job.ID, nil
}
