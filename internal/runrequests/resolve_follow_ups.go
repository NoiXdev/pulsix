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
// template's follow-ups, a retry's (retry not nil) are the rows its run
// group holds, in every state, since the retry copies them all as fresh
// pending rows (CopyRunFollowUps); they keep their depth and path.
func (r *Resolver) followUps(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, retry *retryLookup) (*PictureFollowUps, error) {
	roots, err := r.followUpRoots(ctx, orgID, tmpl, retry)
	if err != nil || len(roots) == 0 {
		return nil, err
	}
	start, err := r.src.Settings.StartsFollowUps(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read follow-up setting: %w", err)
	}
	return BuildFollowUps(ctx, start, roots, r.followUpLookup(orgID))
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
		e := entry{t: FollowUpTemplate{Name: t.Name, Revision: t.Revision, FollowUps: refs}, found: true}
		seen[id] = e
		return e.t, true, nil
	}
}
