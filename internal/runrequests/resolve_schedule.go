package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/cronexpr"
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
// (schedules.Service.Validate, under target) and, where the limit and
// extra vars are the assistant's, by checkScheduleOverrides.
func (r *Resolver) ValidateSchedule(ctx context.Context, orgID, target uuid.UUID, in ScheduleInput) (ScheduleChange, uuid.UUID, int64, error) {
	if r.src.Schedules == nil {
		return ScheduleChange{}, uuid.Nil, 0, errNoSchedules
	}
	ch := ScheduleChange{Action: in.Action}
	switch in.Action {
	case ScheduleCreate:
		spec := *in.Spec
		ch.Spec, ch.AssistantOverrides = &spec, spec.Limit != nil || len(spec.ExtraVars) > 0
	case ScheduleUpdate:
		cur, err := r.src.Schedules.Get(ctx, orgID, target)
		if err != nil {
			return ScheduleChange{}, uuid.Nil, 0, scheduleErr(err)
		}
		spec := mergePatch(cur, *in.Patch)
		ch.Spec, ch.AssistantOverrides = &spec, in.Patch.Limit != nil || in.Patch.ExtraVars != nil
	case ScheduleResume:
		cur, err := r.src.Schedules.Get(ctx, orgID, target)
		if err != nil {
			return ScheduleChange{}, uuid.Nil, 0, scheduleErr(err)
		}
		if !cur.Paused {
			return ScheduleChange{}, uuid.Nil, 0, fmt.Errorf("%w: the schedule is not paused", ErrInvalidInput)
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
// since is ErrChanged, never overwritten); a resume resumes. The
// assistant's limit and extra vars are checked again, as they would be
// now, and stored untrusted.
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
		_, err = r.src.Schedules.Resume(ctx, orgID, *target)
	default:
		return fmt.Errorf("%w: unknown schedule action %q", ErrInvalidInput, ch.Action)
	}
	if err != nil {
		return scheduleErr(err)
	}
	return nil
}

// checkSchedule reads the template a create or update runs and, when the
// limit and extra vars are the assistant's, holds them to its rules
// (checkScheduleOverrides). An update's secret markers are normalised in
// place to the masked marker.
func (r *Resolver) checkSchedule(ctx context.Context, orgID uuid.UUID, ch ScheduleChange) (templates.Template, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, ch.Spec.TemplateID)
	if err != nil {
		return templates.Template{}, mapNotFound(err)
	}
	if !ch.AssistantOverrides {
		return tmpl, nil
	}
	vars, err := r.checkScheduleOverrides(ctx, orgID, tmpl, *ch.Spec, ch.Action == ScheduleUpdate)
	if err != nil {
		return templates.Template{}, err
	}
	ch.Spec.ExtraVars = vars
	return tmpl, nil
}

// checkScheduleOverrides holds a schedule request's limit and extra vars
// to the rules a run request's are held to (checkOverrides), since the
// schedule's runs carry them as untrusted values:
//   - the limit is a plain host pattern (ansiblevars.CheckUntrustedLimit:
//     no "@file", regex or subscript);
//   - extra vars need the template's extra vars schema, and none is or
//     holds a secret: on an update a secret field may only be kept, given
//     as {"$secret":"keep"} (with "from" its own path, if any) or as the
//     masked {"$secret":"set"} a read shows; both are returned as the
//     masked marker, which the schedule's save resolves against the
//     stored value at the same path;
//   - no name Ansible or Pulsix acts on (ansiblevars.CheckRequestedName),
//     no "__ansible" key or deep nesting (ansiblevars.CheckUntrusted), no
//     Pulsix secret marker outside a kept secret, no malformed "__ansible"
//     object in the template's own vars;
//   - no extra var replaces a secret the inventory of the template's
//     targets or its playbook holds (checkSecretShadows).
//
// No refusal echoes a value.
func (r *Resolver) checkScheduleOverrides(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, spec ScheduleSpec, update bool) (map[string]any, error) {
	if spec.Limit != nil {
		if err := ansiblevars.CheckUntrustedLimit(*spec.Limit); err != nil {
			return nil, fmt.Errorf("%w: limit: %w", ErrInvalidInput, err)
		}
	}
	if len(spec.ExtraVars) == 0 {
		return spec.ExtraVars, nil
	}
	if tmpl.ExtraVarsSchema == nil {
		return nil, fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, slices.Min(slices.Collect(maps.Keys(spec.ExtraVars))), errNoDeclaredVars)
	}
	var refused []string
	kept := map[string]bool{}
	vars, err := schema.MapSecrets(tmpl.ExtraVarsSchema.Fields, spec.ExtraVars, func(path string, v any) (any, error) {
		if update && keepsSecret(v, path) {
			kept[topName(path)] = true
			return schema.Mask(), nil
		}
		refused = append(refused, path)
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	if len(refused) > 0 {
		slices.Sort(refused)
		return nil, fmt.Errorf("%w: extra var %s is a secret (or holds one), which cannot be set here; set it on the schedule or the template, or keep the stored one with {\"$secret\":\"keep\"} on an update", ErrInvalidInput, refused[0])
	}
	plain := map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if err := ansiblevars.CheckRequestedName(name); err != nil {
			return nil, fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
		if err := ansiblevars.CheckUntrusted(vars[name]); err != nil {
			return nil, fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
		if kept[name] {
			continue
		}
		if holdsMarker(vars[name]) {
			return nil, fmt.Errorf("%w: extra var %s holds a \"$sealed\" or \"$secret\" object, which only stands for a stored secret", ErrInvalidInput, name)
		}
		plain[name] = vars[name]
	}
	if err := checkTemplateMarkers(tmpl); err != nil {
		return nil, err
	}
	if len(plain) > 0 {
		targets, err := r.src.Jobs.LimitedRunTargets(ctx, orgID, tmpl.ID)
		if err != nil {
			return nil, mapNotFound(err)
		}
		if err := r.checkSecretShadows(ctx, orgID, tmpl, &Overrides{ExtraVars: plain}, targets); err != nil {
			return nil, err
		}
	}
	return vars, nil
}

// keepsSecret reports whether v keeps the stored secret at path: the
// masked marker a read shows, or a keep marker without "from" or with
// "from" path itself.
func keepsSecret(v any, path string) bool {
	m, ok := v.(map[string]any)
	if !ok || m["$secret"] == nil {
		return false
	}
	switch {
	case len(m) == 1 && (m["$secret"] == "set" || m["$secret"] == "keep"):
		return true
	case len(m) == 2 && m["$secret"] == "keep":
		from, isStr := m["from"].(string)
		return isStr && from == path
	}
	return false
}

// topName is the top-level variable a schema path starts with.
func topName(path string) string {
	for i, c := range path {
		if c == '.' || c == '[' {
			return path[:i]
		}
	}
	return path
}

// mergePatch is the schedule cur with patch applied: the request's
// complete new schedule. Secrets cur keeps appear as the masked marker.
func mergePatch(cur schedules.Schedule, patch SchedulePatch) ScheduleSpec {
	spec := ScheduleSpec{
		TemplateID: cur.TemplateID, Name: cur.Name, Mode: cur.Mode, Cron: cur.Cron, Simple: cur.Simple,
		Timezone: cur.Timezone, Guards: cur.Guards, Limit: cur.Overrides.Limit, ExtraVars: schedules.MaskSealed(cur.Overrides.ExtraVars),
	}
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

// scheduleInput is the schedule service's input for a create or update.
func scheduleInput(ch ScheduleChange) schedules.Input {
	s := ch.Spec
	return schedules.Input{
		TemplateID: s.TemplateID, Name: s.Name, Mode: s.Mode, Cron: s.Cron, Simple: s.Simple, Timezone: s.Timezone,
		Guards: s.Guards, Overrides: templates.Overrides{Limit: s.Limit, ExtraVars: s.ExtraVars},
		Untrusted: ch.AssistantOverrides,
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
	ps := &PictureSchedule{Action: ch.Action, ScheduleID: req.TargetID.String(), AssistantOverrides: ch.AssistantOverrides}
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
			state := scheduleState(ScheduleSpec{
				TemplateID: cur.TemplateID, Name: cur.Name, Mode: cur.Mode, Cron: cur.Cron, Simple: cur.Simple,
				Timezone: cur.Timezone, Guards: cur.Guards, Limit: cur.Overrides.Limit, ExtraVars: schedules.MaskSealed(cur.Overrides.ExtraVars),
			}, name)
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
	p.Schedule = ps
	return p, nil
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
