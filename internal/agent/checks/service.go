package checks

import (
	"context"
	"fmt"
)

// unitState is the minimal systemd unit state the service plugin needs —
// package-local so this shared file never imports the platform-specific
// D-Bus client (service_linux.go does that; service_stub.go doesn't need
// it at all), keeping the plugin's logic testable on every platform.
type unitState struct {
	Name        string
	LoadState   string
	ActiveState string
}

// unitLister is the narrow provider seam over systemd unit lookups — the
// facts-style seam for D-Bus. service_linux.go wires the real systemd
// connection; service_stub.go wires a stub that always errors; tests
// inject a fixture.
type unitLister interface {
	ListUnits(ctx context.Context, units []string) ([]unitState, error)
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
	return servicePlugin{Lister: newSystemdLister()}
}

func (servicePlugin) Name() string { return "service" }

// Collect reads the "units" param (required by internal/checks.
// validateParams, but this plugin stays defensive: an absent or empty list
// just means nothing to check, not an error). Every requested unit is
// looked up in one batched call — a fault there (no systemd, no D-Bus, the
// non-Linux stub) fails every unit together, since it is genuinely one
// round trip; a unit the call didn't return anything for reports "unknown"
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

	states, err := p.Lister.ListUnits(ctx, units)
	if err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}

	byName := make(map[string]unitState, len(states))
	for _, s := range states {
		byName[s.Name] = s
	}

	obs := make([]Observation, 0, len(units))
	for _, u := range units {
		obs = append(obs, labelObservation(u, "service.state", serviceStateLabel(byName[u])))
	}
	return obs, nil
}

// serviceStateLabel maps a raw systemd unit state to the plugin's fixed
// label set. A unit the lister never resolved (name absent from the
// response, or explicitly not-found — what a made-up unit name reports)
// and any ActiveState outside the four systemd states this plugin knows
// both collapse to "unknown", never an error: an unrecognized state is
// itself the observation.
func serviceStateLabel(s unitState) string {
	if s.Name == "" || s.LoadState == "not-found" {
		return "unknown"
	}
	switch s.ActiveState {
	case "active", "inactive", "failed", "activating":
		return s.ActiveState
	default:
		return "unknown"
	}
}

var _ Plugin = servicePlugin{}
