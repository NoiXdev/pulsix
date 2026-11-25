package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// errNoOverrideValidator means the resolver was wired without
// Sources.Overrides; a request with a limit or extra vars is then refused
// rather than taken unchecked.
var errNoOverrideValidator = errors.New("run request overrides: no validator")

// checkOverrides checks a request's limit and extra vars against tmpl as it
// is now: no extra var is or holds a secret (an assistant never sets one,
// spec 2026-11-25), the template's own override checks pass with secrets
// refused (templates.ValidateOverrides: the limit rules, the reserved
// names, the schema), and the limit can narrow the template's (runLimit).
// Hosts are checked against the targets by ValidateRun only: one that left
// the scope since is simply not in the picture's targets any more.
func (r *Resolver) checkOverrides(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, ov *Overrides) error {
	if len(ov.ExtraVars) == 0 && ov.Limit == nil {
		return nil
	}
	secret := templates.SecretFields(tmpl.ExtraVarsSchema)
	for _, name := range slices.Sorted(maps.Keys(ov.ExtraVars)) {
		if secret[name] {
			return fmt.Errorf("%w: extra var %s is a secret (or holds one); a run request cannot set secrets, set it on the template", ErrInvalidInput, name)
		}
	}
	if r.src.Overrides == nil {
		return errNoOverrideValidator
	}
	_, err := r.src.Overrides.ValidateOverrides(ctx, orgID, tmpl.ID, templates.Overrides{Limit: ov.Limit, ExtraVars: ov.ExtraVars},
		templates.OverrideOptions{RefuseSecrets: true})
	if err != nil {
		if errors.Is(err, templates.ErrNotFound) {
			return mapNotFound(err)
		}
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	if ov.Limit != nil {
		if _, err := runLimit(tmpl.Limit, *ov.Limit); err != nil {
			return err
		}
	}
	return nil
}

// runOverrides is what an approval enqueues for req's overrides, checked
// again against the template as it is now: the run's jobs.RunOverrides
// and the hosts the run is kept to. Those are the picture's targets
// whenever the request narrows (hosts or a limit), so the job's inventory
// holds the hosts the person saw and no other; nil otherwise.
func (r *Resolver) runOverrides(ctx context.Context, orgID uuid.UUID, req Request) (*jobs.RunOverrides, []uuid.UUID, error) {
	ov := req.Overrides
	tmpl, err := r.src.Templates.Get(ctx, orgID, req.TemplateID)
	if err != nil {
		return nil, nil, mapNotFound(err)
	}
	if err := r.checkOverrides(ctx, orgID, tmpl, ov); err != nil {
		return nil, nil, err
	}
	out := &jobs.RunOverrides{Check: ov.Check, Diff: ov.Diff, ExtraVars: maps.Clone(ov.ExtraVars)}
	if ov.Limit != nil {
		limit, err := runLimit(tmpl.Limit, *ov.Limit)
		if err != nil {
			return nil, nil, err
		}
		out.Limit = &limit
	}
	if len(ov.HostIDs) == 0 && ov.Limit == nil {
		return out, nil, nil
	}
	hosts, err := r.runTargets(ctx, orgID, req)
	if err != nil {
		return nil, nil, err
	}
	if len(hosts) == 0 {
		return nil, nil, fmt.Errorf("%w: none of the requested hosts is among the template's targets any more", ErrInvalidInput)
	}
	return out, hosts, nil
}

// pictureOverrides shows ov in p; nothing for a request without overrides.
func pictureOverrides(ov *Overrides, p *Picture) {
	if ov == nil {
		return
	}
	p.CheckMode, p.Diff = ov.Check, ov.Diff
	if ov.Limit != nil {
		p.LimitOverride = *ov.Limit
	}
	if len(ov.ExtraVars) > 0 {
		p.ExtraVarsOverride = maps.Clone(ov.ExtraVars)
	}
	for _, id := range ov.HostIDs {
		p.Hosts = append(p.Hosts, id.String())
	}
}
