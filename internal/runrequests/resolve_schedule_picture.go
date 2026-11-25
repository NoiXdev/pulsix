package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/schedules"
)

// schedulePicture is the picture of a schedule request: its template, and
// the schedule as the approval stores it and as it is now.
func (r *Resolver) schedulePicture(ctx context.Context, orgID uuid.UUID, req Request) (Picture, error) {
	if r.src.Schedules == nil {
		return Picture{}, errNoSchedules
	}
	ch := req.Schedule
	if ch == nil || req.TargetID == nil {
		return Picture{}, fmt.Errorf("schedule request %s has no schedule or target", req.ID)
	}
	tmpl, err := r.src.Templates.Get(ctx, orgID, req.TemplateID)
	if err != nil {
		return Picture{}, mapNotFound(err)
	}
	p := Picture{
		TemplateID: tmpl.ID.String(), TemplateName: tmpl.Name, TemplateRevision: tmpl.Revision,
		Targets: []PictureHost{}, VaultIdentities: []PictureVault{}, ExtraVars: map[string]any{},
	}
	ps := &PictureSchedule{Action: ch.Action, ScheduleID: req.TargetID.String(), AssistantLimit: ch.AssistantLimit, AssistantExtraVars: ch.AssistantExtraVars}
	paused := false
	if ch.Action != ScheduleCreate {
		cur, err := r.src.Schedules.Get(ctx, orgID, *req.TargetID)
		switch {
		case errors.Is(err, schedules.ErrNotFound):
			ps.Gone = true
		case err != nil:
			return Picture{}, err
		default:
			name := tmpl.Name
			if cur.TemplateID != tmpl.ID {
				name = r.templateName(ctx, orgID, cur.TemplateID)
			}
			state := scheduleState(specOf(cur), name)
			state.Paused, state.Revision, paused = cur.Paused, cur.Revision, cur.Paused
			ps.Current = &state
		}
	}
	if ch.Spec != nil {
		state := scheduleState(*ch.Spec, r.templateName(ctx, orgID, ch.Spec.TemplateID))
		state.ExtraVars = maskKept(state.ExtraVars)
		// An update keeps a paused schedule paused.
		state.Paused = paused
		ps.New = &state
	}
	tid := tmpl.ID
	if ps.OpenChangeReviews, err = r.openChangeReviews(ctx, orgID, &tid, nil); err != nil {
		return Picture{}, err
	}
	p.Schedule = ps
	return p, nil
}

// openChangeReviews names the open reviews of an assistant's changes to
// the template or repository a requested schedule or trigger would run
// (final review 5); none without a source.
func (r *Resolver) openChangeReviews(ctx context.Context, orgID uuid.UUID, templateID, repoID *uuid.UUID) ([]string, error) {
	if r.src.ChangeReviews == nil || (templateID == nil && repoID == nil) {
		return nil, nil
	}
	ids, err := r.src.ChangeReviews.OpenChangeReviews(ctx, orgID, templateID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list open change reviews: %w", err)
	}
	var out []string
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out, nil
}

// scheduleState is spec as the picture shows it. A cron schedule's
// description is the expression's; a simple one's its form's.
func scheduleState(spec ScheduleSpec, templateName string) ScheduleState {
	s := ScheduleState{
		TemplateID: spec.TemplateID.String(), TemplateName: templateName, Name: spec.Name, Mode: string(spec.Mode),
		Cron: spec.Cron, Simple: spec.Simple, Timezone: spec.Timezone, Guards: spec.Guards, Limit: spec.Limit,
		ExtraVars: spec.ExtraVars,
	}
	if spec.Simple != nil {
		if c, err := spec.Simple.Compile(); err == nil {
			s.Cron = c
		}
		s.Description = spec.Simple.Describe()
	} else {
		s.Description = cronexpr.Describe(spec.Cron)
	}
	return s
}

// maskKept shows every keep marker in vars as the masked marker.
func maskKept(vars map[string]any) map[string]any {
	if vars == nil {
		return nil
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = maskKeptValue(v)
	}
	return out
}

func maskKeptValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if x["$secret"] == "keep" {
			return schema.Mask()
		}
		return maskKept(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = maskKeptValue(e)
		}
		return out
	}
	return v
}

// templateName names a template for a picture; its id when it is gone.
func (r *Resolver) templateName(ctx context.Context, orgID, id uuid.UUID) string {
	t, err := r.src.Templates.Get(ctx, orgID, id)
	if err != nil {
		return id.String()
	}
	return t.Name
}
