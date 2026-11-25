package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

// errNoSchedules means the resolver was wired without Sources.Schedules;
// a schedule request is then refused rather than taken unchecked.
var errNoSchedules = errors.New("schedule requests: no schedule source")

// ValidateSchedule implements Automation: a create's schedule, an update's
// patch merged into the schedule as it is now, or a resume of a paused
// schedule, checked the way the schedule routes check a schedule
// (schedules.Service.Validate, under target) and, for each part (limit,
// extra vars) that is the assistant's, by checkSchedule.
func (r *Resolver) ValidateSchedule(ctx context.Context, orgID, target uuid.UUID, in ScheduleInput) (ScheduleChange, uuid.UUID, int64, error) {
	if r.src.Schedules == nil {
		return ScheduleChange{}, uuid.Nil, 0, errNoSchedules
	}
	ch := ScheduleChange{Action: in.Action}
	switch in.Action {
	case ScheduleCreate:
		spec := *in.Spec
		ch.Spec, ch.AssistantLimit, ch.AssistantExtraVars = &spec, spec.Limit != nil, len(spec.ExtraVars) > 0
	case ScheduleUpdate:
		cur, err := r.src.Schedules.Get(ctx, orgID, target)
		if err != nil {
			return ScheduleChange{}, uuid.Nil, 0, scheduleErr(err)
		}
		spec := mergePatch(cur, *in.Patch)
		// A part the patch leaves out is copied with the trust it has now
		// (review finding 1): the request carries it, so clearing the
		// schedule's overrides before the approval cannot make an
		// assistant's copied value a trusted one.
		ch.Spec = &spec
		ch.AssistantLimit = (in.Patch.Limit != nil || cur.UntrustedLimit) && spec.Limit != nil
		ch.AssistantExtraVars = (in.Patch.ExtraVars != nil || cur.UntrustedExtraVars) && len(spec.ExtraVars) > 0
		if scheduleUnchanged(cur, ch) {
			return ScheduleChange{}, uuid.Nil, 0, errNothingToChange
		}
	case ScheduleResume:
		cur, err := r.src.Schedules.Get(ctx, orgID, target)
		if err != nil {
			return ScheduleChange{}, uuid.Nil, 0, scheduleErr(err)
		}
		if !cur.Paused {
			return ScheduleChange{}, uuid.Nil, 0, fmt.Errorf("%w: the schedule is not paused", ErrInvalidInput)
		}
		if cur.PausedReason == schedules.PausedForReview {
			return ScheduleChange{}, uuid.Nil, 0, errScheduleUnderReview
		}
		tmpl, err := r.src.Templates.Get(ctx, orgID, cur.TemplateID)
		if err != nil {
			return ScheduleChange{}, uuid.Nil, 0, mapNotFound(err)
		}
		return ch, tmpl.ID, tmpl.Revision, nil
	default:
		return ScheduleChange{}, uuid.Nil, 0, fmt.Errorf("%w: unknown schedule action %q", ErrInvalidInput, in.Action)
	}
	tmpl, err := r.checkSchedule(ctx, orgID, ch)
	if err != nil {
		return ScheduleChange{}, uuid.Nil, 0, err
	}
	if err := r.src.Schedules.Validate(ctx, orgID, target, scheduleInput(ch)); err != nil {
		return ScheduleChange{}, uuid.Nil, 0, scheduleErr(err)
	}
	return ch, tmpl.ID, tmpl.Revision, nil
}

// ApplySchedule implements Automation: what the approved picture p shows,
// as actor. A create stores the schedule under the request's target id;
// an update writes at the revision the person saw (a schedule changed
// since is ErrChanged, never overwritten); a resume resumes at the
// revision the person saw too. The assistant's parts are checked again,
// as they would be now, and stored untrusted. The approval runs it in the
// transaction that records the decision.
func (r *Resolver) ApplySchedule(ctx context.Context, orgID uuid.UUID, req Request, p PictureSchedule, actor uuid.UUID) error {
	if r.src.Schedules == nil {
		return errNoSchedules
	}
	ch, target := req.Schedule, req.TargetID
	if ch == nil || target == nil {
		return fmt.Errorf("schedule request %s has no schedule or target", req.ID)
	}
	var err error
	switch ch.Action {
	case ScheduleCreate:
		if _, err = r.checkSchedule(ctx, orgID, *ch); err == nil {
			_, err = r.src.Schedules.CreateAs(ctx, orgID, *target, actor, scheduleInput(*ch))
		}
	case ScheduleUpdate:
		if p.Current == nil {
			return errScheduleGone
		}
		if _, err = r.checkSchedule(ctx, orgID, *ch); err == nil {
			_, err = r.src.Schedules.Update(ctx, orgID, *target, p.Current.Revision, scheduleInput(*ch))
		}
	case ScheduleResume:
		if p.Current == nil {
			return errScheduleGone
		}
		// A change an assistant made since paused it for review: that
		// review resumes it, not a resume the assistant asked for.
		cur, gerr := r.src.Schedules.Get(ctx, orgID, *target)
		if gerr != nil {
			return scheduleErr(gerr)
		}
		if cur.PausedReason == schedules.PausedForReview {
			return errScheduleUnderReview
		}
		_, err = r.src.Schedules.ResumeAt(ctx, orgID, *target, p.Current.Revision)
	default:
		return fmt.Errorf("%w: unknown schedule action %q", ErrInvalidInput, ch.Action)
	}
	if err != nil {
		return scheduleErr(err)
	}
	return nil
}

// errNothingToChange refuses an update request that leaves its schedule or
// trigger as it is: there is nothing for a person to approve.
var errNothingToChange = fmt.Errorf("%w: nothing to change: the update leaves it as it is", ErrInvalidInput)

// scheduleUnchanged reports whether the update ch leaves the schedule cur as
// it is: the same settings (kept secrets compared masked) and the same
// trust of its limit and extra vars. A difference only in how a value is
// written (a number's form, a default spelled out) counts as a change, so a
// request is never refused for one that is real.
func scheduleUnchanged(cur schedules.Schedule, ch ScheduleChange) bool {
	now := mergePatch(cur, SchedulePatch{})
	next := *ch.Spec
	now.ExtraVars, next.ExtraVars = maskKept(now.ExtraVars), maskKept(next.ExtraVars)
	return reflect.DeepEqual(now, next) &&
		ch.AssistantLimit == (cur.UntrustedLimit && now.Limit != nil) &&
		ch.AssistantExtraVars == (cur.UntrustedExtraVars && len(now.ExtraVars) > 0)
}

// scheduleInput is the schedule service's input for a create or update.
func scheduleInput(ch ScheduleChange) schedules.Input {
	s := ch.Spec
	return schedules.Input{
		TemplateID: s.TemplateID, Name: s.Name, Mode: s.Mode, Cron: s.Cron, Simple: s.Simple, Timezone: s.Timezone,
		Guards: s.Guards, Overrides: templates.Overrides{Limit: s.Limit, ExtraVars: s.ExtraVars},
		UntrustedLimit: ch.AssistantLimit, UntrustedExtraVars: ch.AssistantExtraVars,
	}
}

// scheduleErr turns the schedule service's refusals into the request's.
func scheduleErr(err error) error {
	var ve *schema.ValidationError
	switch {
	case errors.As(err, &ve):
		return fmt.Errorf("%w: extra vars: %s", ErrInvalidInput, ve.Error())
	case errors.Is(err, schedules.ErrNotFound):
		return fmt.Errorf("%w: schedule not found", ErrNotFound)
	case errors.Is(err, schedules.ErrNameTaken):
		return fmt.Errorf("%w: a schedule with this name already exists", ErrInvalidInput)
	case errors.Is(err, schedules.ErrRevisionMismatch):
		return ErrChanged
	case errors.Is(err, schedules.ErrInvalidInput):
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	case errors.Is(err, templates.ErrNotFound), errors.Is(err, templates.ErrInvalidInput):
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	return err
}

// specOf is the stored schedule cur as a spec, secrets masked.
func specOf(cur schedules.Schedule) ScheduleSpec {
	return ScheduleSpec{
		TemplateID: cur.TemplateID, Name: cur.Name, Mode: cur.Mode, Cron: cur.Cron, Simple: cur.Simple,
		Timezone: cur.Timezone, Guards: cur.Guards, Limit: cur.Overrides.Limit, ExtraVars: schedules.MaskSealed(cur.Overrides.ExtraVars),
	}
}

// mergePatch is the schedule cur with patch applied: the request's
// complete new schedule. Secrets cur keeps appear as the masked marker.
func mergePatch(cur schedules.Schedule, patch SchedulePatch) ScheduleSpec {
	spec := specOf(cur)
	if spec.Mode == schedules.ModeSimple {
		spec.Cron = ""
	}
	if patch.TemplateID != nil {
		spec.TemplateID = *patch.TemplateID
	}
	if patch.Name != nil {
		spec.Name = *patch.Name
	}
	switch {
	case patch.Mode != nil:
		spec.Mode = *patch.Mode
	case patch.Cron != nil:
		spec.Mode = schedules.ModeCron
	case patch.Simple != nil:
		spec.Mode = schedules.ModeSimple
	}
	if patch.Cron != nil {
		spec.Cron = *patch.Cron
	}
	if patch.Simple != nil {
		spec.Simple = patch.Simple
	}
	if spec.Mode == schedules.ModeCron {
		spec.Simple = nil
		if patch.Cron == nil && cur.Mode == schedules.ModeSimple {
			spec.Cron = cur.Cron
		}
	} else {
		spec.Cron = ""
	}
	if patch.Timezone != nil {
		spec.Timezone = *patch.Timezone
	}
	if patch.Guards != nil {
		spec.Guards = *patch.Guards
	}
	if patch.Limit != nil {
		spec.Limit = patch.Limit
		if *patch.Limit == "" {
			spec.Limit = nil
		}
	}
	if patch.ExtraVars != nil {
		spec.ExtraVars = maps.Clone(patch.ExtraVars)
	}
	return spec
}
