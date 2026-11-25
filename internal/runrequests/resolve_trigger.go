package runrequests

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/triggers"
)

// errNoTriggers means the resolver was wired without Sources.Triggers; a
// trigger request is then refused rather than taken unchecked.
var errNoTriggers = errors.New("trigger requests: no trigger source")

// ValidateTrigger implements Automation: a create's trigger or an update's
// patch merged into the trigger as it is now, checked the way the trigger
// routes check a trigger (triggers.Service.Validate under target: the
// settings, the target, a mapping's targets by the execution-level name
// rule, a name no other trigger has) and stored as the service would store
// it. A rotation changes only the secret and names the trigger as it is.
func (r *Resolver) ValidateTrigger(ctx context.Context, orgID, target uuid.UUID, in TriggerInput) (TriggerChange, uuid.UUID, int64, error) {
	if r.src.Triggers == nil {
		return TriggerChange{}, uuid.Nil, 0, errNoTriggers
	}
	ch := TriggerChange{Action: in.Action}
	var spec TriggerSpec
	var cur triggers.Trigger
	switch in.Action {
	case TriggerCreate:
		spec = *in.Spec
	case TriggerUpdate:
		var err error
		if cur, err = r.src.Triggers.Get(ctx, orgID, target); err != nil {
			return TriggerChange{}, uuid.Nil, 0, triggerErr(err)
		}
		spec = mergeTriggerPatch(triggerSpecOf(cur), *in.Patch)
		ch.KeepEnabled = in.Patch.Enabled == nil
		if cur.DisabledReason == triggers.DisabledForReview && spec.Enabled {
			return TriggerChange{}, uuid.Nil, 0, errTriggerUnderReview
		}
	case TriggerRotate:
		cur, err := r.src.Triggers.Get(ctx, orgID, target)
		if err != nil {
			return TriggerChange{}, uuid.Nil, 0, triggerErr(err)
		}
		ch.Kind = cur.Kind
		tmpl, rev, err := r.triggerTemplate(ctx, orgID, cur.Kind, cur.TemplateID)
		return ch, tmpl, rev, err
	default:
		return TriggerChange{}, uuid.Nil, 0, fmt.Errorf("%w: unknown trigger action %q", ErrInvalidInput, in.Action)
	}
	t, err := r.src.Triggers.Validate(ctx, orgID, target, triggerInput(spec))
	if err != nil {
		return TriggerChange{}, uuid.Nil, 0, triggerErr(err)
	}
	stored := triggerSpecOf(t)
	if in.Action == TriggerUpdate && reflect.DeepEqual(stored, triggerSpecOf(cur)) {
		return TriggerChange{}, uuid.Nil, 0, errNothingToChange
	}
	ch.Kind, ch.Spec = t.Kind, &stored
	tmpl, rev, err := r.triggerTemplate(ctx, orgID, t.Kind, t.TemplateID)
	return ch, tmpl, rev, err
}

// triggerTemplate is the template a trigger runs with its revision;
// uuid.Nil and 0 for a repository sync trigger.
func (r *Resolver) triggerTemplate(ctx context.Context, orgID uuid.UUID, kind triggers.Kind, id *uuid.UUID) (uuid.UUID, int64, error) {
	if kind != triggers.KindTemplateRun || id == nil {
		return uuid.Nil, 0, nil
	}
	tmpl, err := r.src.Templates.Get(ctx, orgID, *id)
	if err != nil {
		return uuid.Nil, 0, mapNotFound(err)
	}
	return tmpl.ID, tmpl.Revision, nil
}

// ApplyTrigger implements Automation: what the approved picture p shows, as
// actor. A create stores the trigger under the request's target id (the
// trigger service checks it again, as it is now); an update writes at the
// revision the person saw, and a rotation replaces the secret of that
// revision only (a trigger changed since is ErrChanged, never
// overwritten). A rotation, and an update that leaves enabled out, keep
// the trigger's enabled state as the person saw it. The
// approval runs it in the transaction that records the decision; the
// secret it returns goes to the approval's response only.
func (r *Resolver) ApplyTrigger(ctx context.Context, orgID uuid.UUID, req Request, p PictureTrigger, actor uuid.UUID) (string, error) {
	if r.src.Triggers == nil {
		return "", errNoTriggers
	}
	ch, target := req.Trigger, req.TargetID
	if ch == nil || target == nil {
		return "", fmt.Errorf("trigger request %s has no trigger or target", req.ID)
	}
	var secret string
	var err error
	switch ch.Action {
	case TriggerCreate:
		if ch.Spec == nil {
			return "", fmt.Errorf("trigger request %s has no trigger", req.ID)
		}
		var c triggers.Created
		c, err = r.src.Triggers.CreateAs(ctx, orgID, *target, actor, triggerInput(*ch.Spec))
		secret = c.Secret
	case TriggerUpdate:
		if p.Current == nil || ch.Spec == nil {
			return "", errTriggerGone
		}
		spec := *ch.Spec
		if ch.KeepEnabled {
			spec.Enabled = p.Current.Enabled
		}
		// A change an assistant made since disabled it for review: that
		// review enables it, not an update the assistant asked for.
		if spec.Enabled {
			cur, gerr := r.src.Triggers.Get(ctx, orgID, *target)
			if gerr == nil && cur.DisabledReason == triggers.DisabledForReview {
				return "", errTriggerUnderReview
			}
		}
		_, err = r.src.Triggers.Update(ctx, orgID, *target, p.Current.Revision, triggerInput(spec))
	case TriggerRotate:
		if p.Current == nil {
			return "", errTriggerGone
		}
		var c triggers.Created
		c, err = r.src.Triggers.RotateSecretAt(ctx, orgID, *target, p.Current.Revision)
		secret = c.Secret
	default:
		return "", fmt.Errorf("%w: unknown trigger action %q", ErrInvalidInput, ch.Action)
	}
	if errors.Is(err, triggers.ErrNotFound) {
		return "", errTriggerGone
	}
	if err != nil {
		return "", triggerErr(err)
	}
	return secret, nil
}

// triggerErr turns the trigger service's refusals into the request's.
func triggerErr(err error) error {
	switch {
	case errors.Is(err, triggers.ErrNotFound):
		return fmt.Errorf("%w: trigger not found", ErrNotFound)
	case errors.Is(err, triggers.ErrNameTaken):
		return fmt.Errorf("%w: a trigger with this name already exists", ErrInvalidInput)
	case errors.Is(err, triggers.ErrRevisionMismatch):
		return ErrChanged
	case errors.Is(err, triggers.ErrInvalidInput):
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	return err
}

// triggerInput is the trigger service's input for spec.
func triggerInput(s TriggerSpec) triggers.Input {
	return triggers.Input{
		Kind: s.Kind, TemplateID: s.TemplateID, RepoID: s.RepoID, Name: s.Name, Enabled: s.Enabled, Preset: s.Preset,
		TimestampHeader: s.TimestampHeader, AllowCIDRs: slices.Clone(s.AllowCIDRs), Mapping: s.Mapping, OnActive: s.OnActive,
		RateLimit: s.RateLimit,
	}
}

// triggerSpecOf is the stored trigger t as a spec.
func triggerSpecOf(t triggers.Trigger) TriggerSpec {
	return TriggerSpec{
		Kind: t.Kind, TemplateID: t.TemplateID, RepoID: t.RepoID, Name: t.Name, Enabled: t.Enabled, Preset: t.Preset,
		TimestampHeader: t.TimestampHeader, AllowCIDRs: prefixStrings(t.AllowCIDRs), Mapping: t.Mapping, OnActive: t.OnActive,
		RateLimit: t.RateLimit,
	}
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// mergeTriggerPatch is the trigger spec cur with patch applied: the
// request's complete new trigger.
func mergeTriggerPatch(cur TriggerSpec, patch TriggerPatch) TriggerSpec {
	spec := cur
	if patch.Name != nil {
		spec.Name = *patch.Name
	}
	if patch.Enabled != nil {
		spec.Enabled = *patch.Enabled
	}
	if patch.Preset != nil {
		spec.Preset = *patch.Preset
	}
	if patch.TimestampHeader != nil {
		spec.TimestampHeader = *patch.TimestampHeader
	}
	if patch.AllowCIDRs != nil {
		spec.AllowCIDRs = slices.Clone(*patch.AllowCIDRs)
	}
	if patch.Mapping != nil {
		spec.Mapping = *patch.Mapping
	}
	if patch.OnActive != nil {
		spec.OnActive = *patch.OnActive
	}
	if patch.RateLimit != nil {
		spec.RateLimit = *patch.RateLimit
	}
	return spec
}
