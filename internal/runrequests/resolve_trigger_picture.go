package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/triggers"
)

// triggerPicture is the picture of a trigger request: the template a
// template run trigger runs (a repository sync trigger names none), the
// URL senders post to, and the trigger as the approval stores it and as it
// is now. It never holds the secret.
func (r *Resolver) triggerPicture(ctx context.Context, orgID uuid.UUID, req Request) (Picture, error) {
	if r.src.Triggers == nil {
		return Picture{}, errNoTriggers
	}
	ch := req.Trigger
	if ch == nil || req.TargetID == nil {
		return Picture{}, fmt.Errorf("trigger request %s has no trigger or target", req.ID)
	}
	p := Picture{Targets: []PictureHost{}, VaultIdentities: []PictureVault{}, ExtraVars: map[string]any{}}
	if req.TemplateID != uuid.Nil {
		tmpl, err := r.src.Templates.Get(ctx, orgID, req.TemplateID)
		if err != nil {
			return Picture{}, mapNotFound(err)
		}
		p.TemplateID, p.TemplateName, p.TemplateRevision = tmpl.ID.String(), tmpl.Name, tmpl.Revision
	}
	pt := &PictureTrigger{
		Action: ch.Action, TriggerID: req.TargetID.String(),
		URL: r.src.Triggers.URL(triggers.Trigger{ID: *req.TargetID, Kind: ch.Kind}),
	}
	if ch.Action != TriggerCreate {
		cur, err := r.src.Triggers.Get(ctx, orgID, *req.TargetID)
		switch {
		case errors.Is(err, triggers.ErrNotFound):
			pt.Gone = true
		case err != nil:
			return Picture{}, err
		default:
			state, found, err := r.triggerState(ctx, orgID, triggerSpecOf(cur))
			if err != nil {
				return Picture{}, err
			}
			state.Revision = cur.Revision
			pt.Current, pt.Gone = &state, !found
		}
	}
	if ch.Spec != nil {
		state, found, err := r.triggerState(ctx, orgID, *ch.Spec)
		if err != nil {
			return Picture{}, err
		}
		// An update that leaves enabled out keeps the trigger's state.
		if ch.KeepEnabled && pt.Current != nil {
			state.Enabled = pt.Current.Enabled
		}
		pt.New, pt.Gone = &state, pt.Gone || !found
	}
	var tmpl, repo *uuid.UUID
	if req.TemplateID != uuid.Nil {
		tid := req.TemplateID
		tmpl = &tid
	}
	for _, s := range []*TriggerState{pt.New, pt.Current} {
		if s != nil && s.RepoID != "" {
			if rid, err := uuid.Parse(s.RepoID); err == nil {
				repo = &rid
			}
		}
	}
	var err error
	if pt.OpenChangeReviews, err = r.openChangeReviews(ctx, orgID, tmpl, repo); err != nil {
		return Picture{}, err
	}
	p.Trigger = pt
	return p, nil
}

// triggerState is spec as the picture shows it, and whether what it
// starts still exists. A repository appears with its URL and branch, which
// decide what its sync fetches, so a change of either changes the picture.
func (r *Resolver) triggerState(ctx context.Context, orgID uuid.UUID, spec TriggerSpec) (TriggerState, bool, error) {
	s := TriggerState{
		Kind: string(spec.Kind), Name: spec.Name, Enabled: spec.Enabled, Preset: string(spec.Preset),
		SignatureHeader: triggers.Trigger{Preset: spec.Preset}.SignatureHeader(), TimestampHeader: spec.TimestampHeader,
		AllowCIDRs: spec.AllowCIDRs, Mapping: spec.Mapping, OnActive: string(spec.OnActive), RateLimit: spec.RateLimit,
	}
	if s.AllowCIDRs == nil {
		s.AllowCIDRs = []string{}
	}
	if spec.TemplateID != nil {
		s.TemplateID, s.TemplateName = spec.TemplateID.String(), r.templateName(ctx, orgID, *spec.TemplateID)
	}
	if spec.RepoID != nil {
		s.RepoID = spec.RepoID.String()
		repo, err := r.src.Repos.Get(ctx, orgID, *spec.RepoID)
		if errors.Is(err, repos.ErrNotFound) {
			return s, false, nil
		}
		if err != nil {
			return TriggerState{}, false, err
		}
		s.RepoName, s.RepoURL, s.RepoBranch = repo.Name, repo.URL, repo.Branch
	}
	return s, true, nil
}
