package probes

import (
	"context"
	"errors"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/orgsecrets"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/all"
)

// The errors a probe reports when its check's secret cannot be handed
// over. The store's own error is never one of them: it may carry driver
// details, so it is logged instead.
const (
	errNoSecretStore = "no secret store"
	errSecretStore   = "the secret store could not be read"
)

// secretNotDefined is the error for a secret the organization does not
// store; the host page warns with the same text (checks.MissingSecret).
// The name is format-restricted and never a value.
func secretNotDefined(name string) string {
	return `secret "` + name + `" is not defined`
}

// secretName is the secret check names: "" when its plugin takes none
// or the check names none (it then runs without credentials).
func (r *Runner) secretName(c checks.Resolved) string {
	d, ok := r.describe(c.Plugin)
	if !ok {
		return ""
	}
	name, _ := d.SecretNameOf(c.Params)
	return name
}

// managerOnly reports whether c's check is probed only from the manager
// (checkplugins.Descriptor.ProbedOnlyFromManager): it names a secret, or
// it is an http check with a custom request (backlog 3.84). A plugin no
// descriptor knows is not.
func (r *Runner) managerOnly(c checks.Resolved) bool {
	d, ok := r.describe(c.Plugin)
	return ok && d.ProbedOnlyFromManager(c.Params)
}

// secretFor returns the fields of the secret t's check names (t.secret),
// nil when it names none. When the secret cannot be had it returns
// instead the observations of a probe that could not be attempted, with
// the reason on every metric. The fields go to the probe as their own
// argument and nowhere else: never into params, a log or an observation.
func (r *Runner) secretFor(ctx context.Context, s slot, t target) (map[string]string, []probe.Observation) {
	if t.secret == "" {
		return nil, nil
	}
	if r.secrets == nil {
		return nil, r.unattempted(t, errNoSecretStore)
	}
	fields, err := r.secrets.Fields(orgScope(ctx, t.org), t.org, t.secret)
	switch {
	case errors.Is(err, orgsecrets.ErrNotFound):
		return nil, r.unattempted(t, secretNotDefined(t.secret))
	case err != nil:
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.log.WarnContext(ctx, "probe runner: read secret", "org", t.org, "host", s.host, "check", s.key,
				"secret", t.secret, "err", err)
		}
		return nil, r.unattempted(t, errSecretStore)
	}
	return fields, nil
}

// unattempted is a probe of t that could not be attempted, msg on every
// metric its params would have produced: the plugin's own FailAll where it
// has one (all.FailAll: snmp's host metrics and oids entries, http's
// metrics and json_values entries, what a probe reports for a secret it
// cannot use), otherwise each of its descriptor's metrics.
func (r *Runner) unattempted(t target, msg string) []probe.Observation {
	if obs, ok := all.FailAll(t.check.Plugin, t.check.Params, msg); ok {
		return obs
	}
	d, _ := r.describe(t.check.Plugin)
	metrics := make([]string, len(d.Metrics))
	for i, m := range d.Metrics {
		metrics[i] = m.Name
	}
	return probe.FailAll(msg, metrics...)
}
