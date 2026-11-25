package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
)

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// followUps builds the picture's follow-up tree: a run's roots are its
// template's follow-ups, a retry's (retry not nil) are the rows its run
// group holds, in every state, since the retry copies them all as fresh
// pending rows (CopyRunFollowUps); they keep their depth and path.
//
// pinRepo is the repository the requested run is pinned to (nil: no pin);
// the tree then says what each follow-up runs.
func (r *Resolver) followUps(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, retry *retryLookup, pinRepo *uuid.UUID) (*PictureFollowUps, error) {
	roots, err := r.followUpRoots(ctx, orgID, tmpl, retry)
	if err != nil || len(roots) == 0 {
		return nil, err
	}
	start, err := r.src.Settings.StartsFollowUps(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read follow-up setting: %w", err)
	}
	return buildFollowUps(ctx, start, roots, r.followUpLookup(orgID), pinRepo)
}

func (r *Resolver) followUpRoots(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, retry *retryLookup) ([]FollowUpRoot, error) {
	if retry == nil {
		roots := make([]FollowUpRoot, len(tmpl.FollowUps))
		for i, f := range tmpl.FollowUps {
			roots[i] = FollowUpRoot{TemplateID: f.TemplateID, Name: f.Name, Depth: 1, Path: []uuid.UUID{tmpl.ID}}
		}
		return roots, nil
	}
	// A retry that can no longer run shows no follow-ups either.
	if retry.err != nil {
		return nil, nil
	}
	rows, err := r.src.FollowUpRuns.ListForGroup(ctx, orgID, retry.group)
	if err != nil {
		return nil, fmt.Errorf("list follow-ups of the run group: %w", err)
	}
	roots := make([]FollowUpRoot, len(rows))
	for i, row := range rows {
		roots[i] = FollowUpRoot{TemplateID: row.TemplateID, Name: row.TemplateName, Depth: row.Depth, Path: row.Path}
	}
	return roots, nil
}

// followUpLookup reads each template at most once per picture: a template
// that several branches name (a diamond) is read the first time only, a
// deleted one included.
func (r *Resolver) followUpLookup(orgID uuid.UUID) FollowUpLookup {
	type entry struct {
		t     FollowUpTemplate
		found bool
	}
	seen := map[uuid.UUID]entry{}
	return func(ctx context.Context, id uuid.UUID) (FollowUpTemplate, bool, error) {
		if e, ok := seen[id]; ok {
			return e.t, e.found, nil
		}
		t, err := r.src.Templates.Get(ctx, orgID, id)
		if errors.Is(err, templates.ErrNotFound) {
			seen[id] = entry{}
			return FollowUpTemplate{}, false, nil
		}
		if err != nil {
			return FollowUpTemplate{}, false, fmt.Errorf("read follow-up template: %w", err)
		}
		refs := make([]FollowUpRef, len(t.FollowUps))
		for i, f := range t.FollowUps {
			refs[i] = FollowUpRef{TemplateID: f.TemplateID, Name: f.Name}
		}
		ft := FollowUpTemplate{Name: t.Name, Revision: t.Revision, FollowUps: refs}
		if ft.RepoID, ft.RepoName, err = r.playbookRepo(ctx, orgID, t.PlaybookID); err != nil {
			return FollowUpTemplate{}, false, err
		}
		e := entry{t: ft, found: true}
		seen[id] = e
		return e.t, true, nil
	}
}

// playbookRepo is the repository a template's playbook comes from, with
// its name: nil for an editor playbook, none registered, or one gone.
func (r *Resolver) playbookRepo(ctx context.Context, orgID, playbookID uuid.UUID) (*uuid.UUID, string, error) {
	if playbookID == uuid.Nil || r.src.Playbooks == nil {
		return nil, "", nil
	}
	pb, err := r.src.Playbooks.Get(ctx, orgID, playbookID)
	if errors.Is(err, playbooks.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read follow-up playbook: %w", err)
	}
	if pb.RepoID == nil {
		return nil, "", nil
	}
	name := pb.RepoID.String()
	repo, err := r.src.Repos.Get(ctx, orgID, *pb.RepoID)
	switch {
	case err == nil:
		name = repo.Name
	case !errors.Is(err, repos.ErrNotFound):
		return nil, "", fmt.Errorf("read follow-up repository: %w", err)
	}
	return pb.RepoID, name, nil
}
