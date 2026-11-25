package runrequests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// errNoOverrideValidator means the resolver was wired without
// Sources.Overrides; a request with a limit or extra vars is then refused
// rather than taken unchecked.
var errNoOverrideValidator = errors.New("run request overrides: no validator")

// errNoHost refuses overrides that leave the run without a host.
var errNoHost = fmt.Errorf("%w: the requested hosts and limit leave no host of the template's targets", ErrInvalidInput)

// errNoDeclaredVars refuses extra vars on a template without an extra vars
// schema (final review I2, controller ruling).
var errNoDeclaredVars = errors.New("this template declares no extra variables, so a run request cannot set any; declare them on the template first")

// errLimitRule refuses a limit override; it says exactly what a run
// request's limit may hold.
var errLimitRule = fmt.Errorf("%w: limit: only host names and * wildcards, joined by , or :, with ! before a name to exclude it", ErrInvalidInput)

// checkTemplateMarkers refuses a request with extra vars on a template
// whose own extra vars hold an "__ansible" object that is not a
// well-formed marker: the claim of a run with untrusted values refuses
// such a document (jobs.checkWireMarkers), so the request could be
// approved and still never run. The template's values are not echoed.
func checkTemplateMarkers(tmpl templates.Template) error {
	for _, name := range slices.Sorted(maps.Keys(tmpl.ExtraVars)) {
		if err := ansiblevars.CheckMarkers(tmpl.ExtraVars[name]); err != nil {
			return fmt.Errorf("%w: template extra var %s holds an object with an \"__ansible\" key that is not a well-formed marker; a run request with extra vars cannot run with it until the template is fixed", ErrInvalidInput, name)
		}
	}
	return nil
}

// pulsixMarkers are the object keys Pulsix itself gives a meaning to in
// variable values: a sealed secret and a masked or kept one. A request's
// value holding one would read as a secret on the approval page while the
// run receives a literal object, so it is refused.
var pulsixMarkers = []string{"$sealed", "$secret"}

// checkOverrides checks a request's limit and extra vars against tmpl as it
// is now. They are an assistant's input, held to the rules a webhook
// payload is held to:
//   - no extra var is named for something Ansible or Pulsix acts on
//     (ansiblevars.CheckRequestedName: ansible_…, pulsix_…, magic
//     variables, template globals, and playbook keywords too), since
//     Ansible executes some of them whatever their value;
//   - no extra var is or holds a secret (an assistant never sets one, spec
//     2026-11-25), and a template without an extra vars schema takes none
//     at all (errNoDeclaredVars): without declared names nothing tells
//     which name a secret elsewhere hides under;
//   - no value holds an "__ansible…" key or nests past
//     ansiblevars.MaxUntrustedDepth (ansiblevars.CheckUntrusted), so the
//     run can carry it marked unsafe for Ansible, and no value holds a
//     Pulsix secret marker;
//   - the template's own override checks pass with secrets refused and the
//     limit held to plain host patterns (templates.ValidateOverrides with
//     RefuseSecrets and StrictLimit: no "@file", "~regex" or subscripts);
//   - the limit is one the host-name matcher decides (errLimitRule), since
//     it is resolved into hosts (runTargets);
//   - with extra vars, the template's own vars hold no malformed
//     "__ansible" object (checkTemplateMarkers).
//
// No refusal echoes a value. Hosts are checked against the targets by
// ValidateRun only: one that left the scope since is simply not in the
// picture's targets any more.
func (r *Resolver) checkOverrides(ctx context.Context, orgID uuid.UUID, tmpl templates.Template, ov *Overrides) error {
	if len(ov.ExtraVars) == 0 && ov.Limit == nil {
		return nil
	}
	secret := templates.SecretFields(tmpl.ExtraVarsSchema)
	for _, name := range slices.Sorted(maps.Keys(ov.ExtraVars)) {
		if err := ansiblevars.CheckRequestedName(name); err != nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
		if secret[name] {
			return fmt.Errorf("%w: extra var %s is a secret (or holds one); a run request cannot set secrets, set it on the template", ErrInvalidInput, name)
		}
		if err := ansiblevars.CheckUntrusted(ov.ExtraVars[name]); err != nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, name, err)
		}
		if holdsMarker(ov.ExtraVars[name]) {
			return fmt.Errorf("%w: extra var %s holds a \"$sealed\" or \"$secret\" object, which only stands for a stored secret", ErrInvalidInput, name)
		}
	}
	if len(ov.ExtraVars) > 0 {
		if tmpl.ExtraVarsSchema == nil {
			return fmt.Errorf("%w: extra var %s: %w", ErrInvalidInput, slices.Min(slices.Collect(maps.Keys(ov.ExtraVars))), errNoDeclaredVars)
		}
		if err := checkTemplateMarkers(tmpl); err != nil {
			return err
		}
	}
	if ov.Limit != nil {
		// Both rules hold: the webhook rule refuses "@", "~", "[…]" and "?",
		// the matcher "&"; what is left is exactly errLimitRule.
		_, decided := jobs.MatchLimit(*ov.Limit, "x")
		if ansiblevars.CheckUntrustedLimit(*ov.Limit) != nil || !decided {
			return errLimitRule
		}
	}
	if r.src.Overrides == nil {
		return errNoOverrideValidator
	}
	_, err := r.src.Overrides.ValidateOverrides(ctx, orgID, tmpl.ID, templates.Overrides{Limit: ov.Limit, ExtraVars: ov.ExtraVars},
		templates.OverrideOptions{RefuseSecrets: true, StrictLimit: true})
	if err != nil {
		if errors.Is(err, templates.ErrNotFound) {
			return mapNotFound(err)
		}
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	return nil
}

// holdsMarker reports whether v holds, at any depth, an object with a
// Pulsix secret marker key (pulsixMarkers).
func holdsMarker(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if slices.Contains(pulsixMarkers, k) || holdsMarker(item) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(x, holdsMarker)
	}
	return false
}

// runOverrides is what an approval enqueues for req's overrides, checked
// again against the template as it is now: the run's jobs.RunOverrides and
// the hosts the run is kept to, which are always the picture's targets
// (runTargets), so the job's inventory holds the hosts the person saw and
// no other. The request's own limit is spent on those hosts and never
// reaches Ansible: the template's limit applies on top as for any run.
// Extra vars travel Untrusted: the claim marks every string in them unsafe
// for Ansible, so no "{{ … }}" or lookup in them ever runs.
func (r *Resolver) runOverrides(ctx context.Context, orgID uuid.UUID, req Request) (*jobs.RunOverrides, []uuid.UUID, error) {
	ov := req.Overrides
	tmpl, err := r.src.Templates.Get(ctx, orgID, req.TemplateID)
	if err != nil {
		return nil, nil, mapNotFound(err)
	}
	if err := r.checkOverrides(ctx, orgID, tmpl, ov); err != nil {
		return nil, nil, err
	}
	out := &jobs.RunOverrides{Check: ov.Check, Diff: ov.Diff, ExtraVars: maps.Clone(ov.ExtraVars), Untrusted: len(ov.ExtraVars) > 0}
	hosts, err := r.runTargets(ctx, orgID, req)
	if err != nil {
		return nil, nil, err
	}
	if len(hosts) == 0 {
		return nil, nil, errNoHost
	}
	if err := r.checkSecretShadows(ctx, orgID, tmpl, ov, hosts); err != nil {
		return nil, nil, err
	}
	return out, hosts, nil
}

// narrowTargets keeps the ids of ids the request's overrides select: the
// requested hosts, then the hosts whose name its limit matches.
func (r *Resolver) narrowTargets(ctx context.Context, orgID uuid.UUID, ov *Overrides, ids []uuid.UUID) ([]uuid.UUID, error) {
	kept := []uuid.UUID{}
	for _, id := range ids {
		if len(ov.HostIDs) == 0 || slices.Contains(ov.HostIDs, id) {
			kept = append(kept, id)
		}
	}
	if ov.Limit == nil || len(kept) == 0 {
		return kept, nil
	}
	hs, err := r.src.Hosts.ListHostsByIDs(ctx, orgID, kept)
	if err != nil {
		return nil, fmt.Errorf("list target hosts: %w", err)
	}
	match := map[uuid.UUID]bool{}
	for _, h := range hs {
		// checkOverrides only lets a decidable limit through; an
		// undecidable one (a request stored by a build without that
		// check) keeps nothing rather than everything.
		if m, decided := jobs.MatchLimit(*ov.Limit, h.Name); m && decided {
			match[h.ID] = true
		}
	}
	return slices.DeleteFunc(kept, func(id uuid.UUID) bool { return !match[id] }), nil
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
		p.RequestedHostIDs = append(p.RequestedHostIDs, id.String())
	}
}
