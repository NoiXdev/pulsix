package runrequests

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

// checkSchedule reads the template a create or update runs and holds
// each part that is the assistant's to its rules: the limit to a plain
// host pattern, the extra vars to checkScheduleVars, whose secret markers
// are normalised in place to the masked marker. A copied assistant's part
// is checked like a new one, against the template the schedule runs then.
func (r *Resolver) checkSchedule(ctx context.Context, orgID uuid.UUID, ch ScheduleChange) (templates.Template, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, ch.Spec.TemplateID)
	if err != nil {
		return templates.Template{}, mapNotFound(err)
	}
	if ch.AssistantLimit && ch.Spec.Limit != nil {
		if err := ansiblevars.CheckUntrustedLimit(*ch.Spec.Limit); err != nil {
			return templates.Template{}, fmt.Errorf("%w: limit: %w", ErrInvalidInput, err)
		}
	}
	if !ch.AssistantExtraVars {
		return tmpl, nil
	}
	vars, err := r.checkScheduleVars(ctx, orgID, tmpl, ch.Spec.ExtraVars, ch.Action == ScheduleUpdate)
	if err != nil {
		return templates.Template{}, err
	}
	ch.Spec.ExtraVars = vars
	return tmpl, nil
}

// checkScheduleVars holds a schedule request's extra vars that are the
// assistant's to the rules a run request's are held to (checkOverrides),
// since the schedule's runs carry them as untrusted values:
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
func (r *Resolver) checkScheduleVars(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, extraVars map[string]any, update bool) (map[string]any, error) {
	if len(extraVars) == 0 {
		return extraVars, nil
	}
	if tmpl.ExtraVarsSchema == nil {
		return nil, fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, slices.Min(slices.Collect(maps.Keys(extraVars))), errNoDeclaredVars)
	}
	var refused []string
	kept := map[string]bool{}
	vars, err := schema.MapSecrets(tmpl.ExtraVarsSchema.Fields, extraVars, func(path string, v any) (any, error) {
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
