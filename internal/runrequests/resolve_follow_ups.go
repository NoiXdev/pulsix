package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/templates"
)

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// followUps builds the picture's follow-up tree: a run's roots are its
// template's follow-ups, a retry's are the unstarted rows its run group
// holds (they keep their depth and path).
func (r *Resolver) followUps(ctx context.Context, orgID uuid.UUID, req Request, tmpl templates.Template) (*PictureFollowUps, error) {
	roots, err := r.followUpRoots(ctx, orgID, req, tmpl)
	if err != nil || len(roots) == 0 {
		return nil, err
	}
	start, err := r.src.Settings.StartsFollowUps(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read follow-up setting: %w", err)
	}
	return BuildFollowUps(ctx, start, roots, r.followUpLookup(orgID))
}

func (r *Resolver) followUpRoots(ctx context.Context, orgID uuid.UUID, req Request, tmpl templates.Template) ([]FollowUpRoot, error) {
	if req.Kind != KindRetryFailed || req.JobID == nil {
		roots := make([]FollowUpRoot, len(tmpl.FollowUps))
		for i, f := range tmpl.FollowUps {
			roots[i] = FollowUpRoot{TemplateID: f.TemplateID, Name: f.Name, Depth: 1, Path: []uuid.UUID{tmpl.ID}}
		}
		return roots, nil
	}
	// A retry that can no longer run shows no follow-ups either.
	_, group, _, err := r.src.Jobs.RetryTargets(ctx, orgID, *req.JobID)
	if err != nil {
		return nil, nil
	}
	rows, err := r.src.FollowUpRuns.ListForGroup(ctx, orgID, group)
	if err != nil {
		return nil, fmt.Errorf("list follow-ups of the run group: %w", err)
	}
	roots := make([]FollowUpRoot, len(rows))
	for i, row := range rows {
		roots[i] = FollowUpRoot{TemplateID: row.TemplateID, Name: row.TemplateName, Depth: row.Depth, Path: row.Path}
	}
	return roots, nil
}

func (r *Resolver) followUpLookup(orgID uuid.UUID) FollowUpLookup {
	return func(ctx context.Context, id uuid.UUID) (FollowUpTemplate, bool, error) {
		t, err := r.src.Templates.Get(ctx, orgID, id)
		if errors.Is(err, templates.ErrNotFound) {
			return FollowUpTemplate{}, false, nil
		}
		if err != nil {
			return FollowUpTemplate{}, false, fmt.Errorf("read follow-up template: %w", err)
		}
		refs := make([]FollowUpRef, len(t.FollowUps))
		for i, f := range t.FollowUps {
			refs[i] = FollowUpRef{TemplateID: f.TemplateID, Name: f.Name}
		}
		return FollowUpTemplate{Name: t.Name, Revision: t.Revision, FollowUps: refs}, true, nil
	}
}
