package checkplugins

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
)

// Vantage-point parameters of the synthetic checks (backlog 3.13a, spec
// docs/superpowers/specs/2026-11-16-probe-vantages-design.md): a synthetic
// check is probed from the manager and from the agents of monitoring
// groups.
const (
	paramVantageManager = "vantage_manager"
	paramVantageGroups  = "vantage_groups"
	paramAgentsPerGroup = "agents_per_group"
	paramFailPolicy     = "fail_policy"

	// MaxVantageGroups bounds vantage_groups.
	MaxVantageGroups = 8
	// MaxAgentsPerGroup bounds agents_per_group; DefaultAgentsPerGroup is
	// what an absent value means.
	MaxAgentsPerGroup     = 10
	DefaultAgentsPerGroup = 3

	// The fail policies: when the check as a whole is down.
	PolicyAny      = "any"
	PolicyMajority = "majority"
	PolicyAll      = "all"
)

// Vantage is a check's vantage settings with the defaults applied.
type Vantage struct {
	// Manager: the Pulsix server probes too.
	Manager bool
	// Groups: the monitoring groups whose agents probe.
	Groups []uuid.UUID
	// PerGroup: agents chosen per group.
	PerGroup int
	// Policy: PolicyAny, PolicyMajority or PolicyAll.
	Policy string
}

// vantageParams are the four parameters every synthetic descriptor appends
// after its own.
func vantageParams() []ParamSpec {
	return []ParamSpec{
		{Name: paramVantageManager, Type: TypeBool, Default: true},
		{Name: paramVantageGroups, Type: TypeList, MaxEntries: MaxVantageGroups, Format: FormatUUID},
		{Name: paramAgentsPerGroup, Type: TypeInteger, Min: bound(1), Max: bound(MaxAgentsPerGroup), Default: int64(DefaultAgentsPerGroup)},
		{Name: paramFailPolicy, Type: TypeEnum, Options: []string{PolicyAny, PolicyMajority, PolicyAll}, Default: PolicyAny},
	}
}

// withVantage gives a synthetic descriptor the vantage parameters and
// composes its own Check hook with the vantage rules; both run.
func withVantage(d Descriptor) Descriptor {
	d.Params = append(slices.Clone(d.Params), vantageParams()...)
	own := d.Check
	d.Check = func(params map[string]any) error {
		if own != nil {
			if err := own(params); err != nil {
				return err
			}
		}
		return vantageCheck(d, params)
	}
	return d
}

// vantageCheck is the cross-parameter rules; each value already passed its
// own spec. d is the descriptor whose ManagerOnly hook keeps a check on
// the Pulsix server.
func vantageCheck(d Descriptor, params map[string]any) error {
	groups, _ := stringList(params[paramVantageGroups])
	if manager, ok := params[paramVantageManager].(bool); ok && !manager && len(groups) == 0 {
		return errors.New("a check needs at least one vantage point")
	}
	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		if seen[g] {
			return errors.New("vantage_groups names a group twice")
		}
		seen[g] = true
	}
	if secret, _ := params[SecretParamName].(string); secret != "" && len(groups) > 0 {
		return errors.New("checks with a secret are probed only from the Pulsix server")
	}
	if d.ManagerOnly != nil && len(groups) > 0 {
		if reason := d.ManagerOnly(params); reason != "" {
			return fmt.Errorf("checks that %s are probed only from the Pulsix server", reason)
		}
	}
	return nil
}

// VantageOf reads a check's vantage settings out of its params, applying
// the defaults (manager on, 3 agents per group, policy any) to what is
// absent. Params that did not pass Validate yield defaults for the
// unreadable values; unparsable group entries are skipped.
func VantageOf(params map[string]any) Vantage {
	v := Vantage{Manager: true, PerGroup: DefaultAgentsPerGroup, Policy: PolicyAny}
	if m, ok := params[paramVantageManager].(bool); ok {
		v.Manager = m
	}
	if list, ok := stringList(params[paramVantageGroups]); ok {
		for _, s := range list {
			if id, err := uuid.Parse(s); err == nil {
				v.Groups = append(v.Groups, id)
			}
		}
	}
	if n, ok := wholeNumber(params[paramAgentsPerGroup]); ok {
		v.PerGroup = int(n)
	}
	if p, ok := params[paramFailPolicy].(string); ok && p != "" {
		v.Policy = p
	}
	return v
}

// StripVantage returns a copy of params without the four vantage
// parameters; the probe itself never sees them.
func StripVantage(params map[string]any) map[string]any {
	out := maps.Clone(params)
	if out == nil {
		return map[string]any{}
	}
	for _, name := range []string{paramVantageManager, paramVantageGroups, paramAgentsPerGroup, paramFailPolicy} {
		delete(out, name)
	}
	return out
}
