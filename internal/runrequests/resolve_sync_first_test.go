package runrequests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// fakeRepoSyncer starts (or finds) a repository's sync.
type fakeRepoSyncer struct {
	job     uuid.UUID
	already bool
	err     error
	repos   []uuid.UUID // the repositories synced
	actors  []uuid.UUID
}

func (f *fakeRepoSyncer) Sync(_ context.Context, _, repoID uuid.UUID, actor *uuid.UUID) (jobs.Job, bool, error) {
	if f.err != nil {
		return jobs.Job{}, false, f.err
	}
	f.repos, f.actors = append(f.repos, repoID), append(f.actors, *actor)
	return jobs.Job{ID: f.job, Kind: jobs.KindSyncRepo}, f.already, nil
}

// TestSyncFirstSyncsTheTemplatesRepository: the sync a sync_first request
// waits for is its template's repository's, started (or found running) as
// the requester; a template whose playbook comes from no repository has
// nothing to sync.
func TestSyncFirstSyncsTheTemplatesRepository(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	actor := id()
	for _, already := range []bool{false, true} {
		w.repoSyncs.already = already
		job, err := w.resolver().SyncFirst(ctx, id(), w.x, actor)
		if err != nil || job == nil || *job != w.repoSyncs.job {
			t.Fatalf("already running %v: job = %v %v", already, job, err)
		}
	}
	if len(w.repoSyncs.repos) != 2 || w.repoSyncs.repos[0] != w.repo || w.repoSyncs.actors[0] != actor {
		t.Fatalf("synced %v as %v", w.repoSyncs.repos, w.repoSyncs.actors)
	}

	// Y has no playbook; an editor playbook has no repository.
	if job, err := w.resolver().SyncFirst(ctx, id(), w.y, actor); err != nil || job != nil {
		t.Fatalf("no playbook = %v %v", job, err)
	}
	editor := "- hosts: all"
	w.pbs[w.pb] = playbooks.Playbook{ID: w.pb, Source: "editor", Inline: &editor}
	if job, err := w.resolver().SyncFirst(ctx, id(), w.x, actor); err != nil || job != nil {
		t.Fatalf("editor playbook = %v %v", job, err)
	}
	if len(w.repoSyncs.repos) != 2 {
		t.Fatalf("synced %v for templates without a repository", w.repoSyncs.repos)
	}

	w = newWorld()
	w.repoSyncs.err = jobs.ErrNoZone
	if _, err := w.resolver().SyncFirst(ctx, id(), w.x, actor); !errors.Is(err, jobs.ErrNoZone) {
		t.Fatalf("sync refused = %v", err)
	}
	if _, err := w.resolver().SyncFirst(ctx, id(), id(), actor); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("unknown template = %v", err)
	}
}
