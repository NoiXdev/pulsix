package checks

import (
	"context"
	"fmt"
)

// unitResult is one unit's outcome: its label, or the error that kept it
// from being looked up. Per-unit so a platform whose service manager is
// queried unit by unit (Windows) can fail one name without the others.
type unitResult struct {
	Label string
	Err   error
}

// unitLister is the narrow provider seam over the platform's service
// manager. The per-platform files (service_linux.go, service_windows.go,
// service_darwin.go, service_stub.go) wire the real adapter; tests inject a
// fixture.
type unitLister interface {
	// States looks every unit up; a whole-lookup failure is the error, a
	// per-unit failure is that unit's Err. Keys are the names as requested.
	States(ctx context.Context, units []string) (map[string]unitResult, error)
}

// servicePlugin reports one label observation per requested unit (item =
// unit name): service.state, one of active/inactive/failed/activating/
// unknown. It is a report of the unit's state, never a judgement — whether
// "inactive" is fine or an emergency is the manager's rule to evaluate,
// not this plugin's.
type servicePlugin struct {
	Lister unitLister
}

func newServicePlugin() Plugin {
	return servicePlugin{Lister: newUnitLister()}
}

func (servicePlugin) Name() string { return "service" }

// Collect reads the "units" param (required by internal/checks.
// validateParams, but this plugin stays defensive: an absent or empty list
// just means nothing to check, not an error). Every requested unit is
// looked up in one call — a fault there (no systemd, no D-Bus, no service
// manager, an unsupported platform) fails every unit together, since it is genuinely one
// round trip, and is reported as each requested unit's own service.state
// error observation rather than nothing at all; a unit the call didn't
// return anything for (the lister itself succeeded) reports "unknown"
// rather than an error, since "unknown" is itself a valid state.
func (p servicePlugin) Collect(ctx context.Context, params map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	units, _, err := paramStrings(params, "units")
	if err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}
	if len(units) == 0 {
		return nil, nil
	}
	if p.Lister == nil {
		return nil, fmt.Errorf("service: no unit lister configured")
	}

	states, err := p.Lister.States(ctx, units)
	if err != nil {
		wrapped := fmt.Errorf("service: %w", err)
		// One D-Bus round trip covers every requested unit, so a fault
		// here is genuinely indivisible — there is no per-unit call to
		// isolate the way disk isolates per-mount faults. Still: every
		// requested unit's own service.state is reported as its own error
		// observation rather than nothing at all, so the manager shows
		// "collection failed" for each instead of leaving every one of
		// them at its last-known (possibly long stale) state.
		obs := make([]Observation, 0, len(units))
		for _, u := range units {
			obs = append(obs, errObservation(u, "service.state", wrapped))
		}
		return obs, wrapped
	}

	obs := make([]Observation, 0, len(units))
	for _, u := range units {
		r, ok := states[u]
		switch {
		case !ok:
			obs = append(obs, labelObservation(u, "service.state", "unknown"))
		case r.Err != nil:
			obs = append(obs, errObservation(u, "service.state", fmt.Errorf("service: %w", r.Err)))
		default:
			obs = append(obs, labelObservation(u, "service.state", r.Label))
		}
	}
	return obs, nil
}

var _ Plugin = servicePlugin{}
