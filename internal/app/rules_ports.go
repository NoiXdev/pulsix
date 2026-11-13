package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/rules"
)

// checkResolver is the slice of checks.Service the rule engine's loader
// needs. Declared as an interface so the adapter below can be tested
// without a database, the same shape maintenance_ports.go's own resolver
// port uses.
type checkResolver interface {
	ResolveHost(ctx context.Context, orgID, hostID uuid.UUID) (checks.ResolvedSet, error)
	GroupsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]checks.Group, error)
}

// rulesHostChecks adapts checks.Service to rules.HostChecks. The edge
// exists here, in the wiring, because internal/rules never imports
// internal/checks — the same reason rules.GroupRank is its own type rather
// than checks.Group (see its doc comment).
type rulesHostChecks struct{ checks checkResolver }

// NewRulesHostChecks wires the adapter Build uses.
func NewRulesHostChecks(svc *checks.Service) rules.HostChecks { return rulesHostChecks{checks: svc} }

// ContextForHost implements rules.HostChecks: the resolved interval per
// check key, whether that check still asks for an item, and the
// monitoring groups whose selector currently matches
// the host.
//
// Both halves come from the very same resolver the agent's own
// configuration push runs on, which is what makes "a host cannot receive a
// group's rules without also receiving that group's checks" true rather
// than merely intended. Neither failure is softened into an empty answer:
// an evaluation that mistook a resolver outage for "this host has no
// checks any more" would take every one of its tuples out of the
// evaluation and delete its roll-up.
func (a rulesHostChecks) ContextForHost(ctx context.Context, orgID, hostID uuid.UUID) (rules.CheckContext, error) {
	set, err := a.checks.ResolveHost(ctx, orgID, hostID)
	if err != nil {
		return rules.CheckContext{}, err
	}
	// Every resolved check, synthetic ones included: the manager's probes
	// write into the same tuples an agent's results do (backlog 3.13).
	all := set.All()
	intervals := make(map[string]time.Duration, len(all))
	asks := make(map[string]func(item string) bool, len(all))
	for _, c := range all {
		intervals[c.Key] = c.Interval
		asks[c.Key] = asksFor(c)
	}
	groups, err := a.checks.GroupsForHost(ctx, orgID, hostID)
	if err != nil {
		return rules.CheckContext{}, err
	}
	ranks := make([]rules.GroupRank, len(groups))
	for i, g := range groups {
		ranks[i] = rules.GroupRank{ID: g.ID, Priority: g.Priority, CreatedAt: g.CreatedAt}
	}
	return rules.CheckContext{Intervals: intervals, Asks: asks, Groups: ranks}, nil
}

// asksFor is c's item predicate for the evaluation (backlog 3.55): the
// rule the host page hides a dropped item's values by
// (checks.Definition.AsksForItem), so both agree on every named item. The
// whole-host item "" is always judged here, whatever the list names: the
// plugins that report on it (temperature, smart, raid) are always asked
// for it anyway, and the ones that never emit it (disk, network,
// service) leave nothing for the guard to change. The host page has no
// such guard; a "" value of those plugins would be hidden there.
func asksFor(c checks.Resolved) func(item string) bool {
	return func(item string) bool {
		return item == "" || checkplugins.AsksForItem(c.Plugin, c.Params, item)
	}
}
