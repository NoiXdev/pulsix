package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/rules"
)

// checkResolver is the slice of checks.Service the rule engine's loader
// needs. Declared as an interface so the adapter below can be tested
// without a database, the same shape maintenance_ports.go's own resolver
// port uses.
type checkResolver interface {
	ResolveHostWithLocal(ctx context.Context, orgID, hostID uuid.UUID) (checks.ResolvedSet, error)
	GroupsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]checks.Group, error)
}

// rulesHostChecks adapts checks.Service to rules.HostChecks. The edge
// exists here, in the wiring, because internal/rules never imports
// internal/checks — the same reason rules.GroupRank is its own type rather
// than checks.Group (see its doc comment).
type rulesHostChecks struct{ checks checkResolver }

// newEvalStore is the evaluator's store as Build wires it: the agents
// list's own online window (agents.StaleAfter) is what decides whether a
// client host's agent is off and the host frozen (backlog 3.57).
// internal/rules never imports internal/agents, so this is the one place
// the two meet; TestEvalStoreJudgesAgentsOnlineLikeTheAgentsList pins it.
func newEvalStore(d *db.DB, hc rules.HostChecks, it rules.ItemStore) *rules.EvalSQLStore {
	return rules.NewEvalSQLStore(d, hc, it, agents.StaleAfter, slog.Default())
}

// NewRulesHostChecks wires the adapter Build uses.
func NewRulesHostChecks(svc *checks.Service) rules.HostChecks { return rulesHostChecks{checks: svc} }

// ContextForHost implements rules.HostChecks: the resolved interval per
// check key, whether that check still asks for an item and for a metric
// (checks.Resolved.AsksForMetric, the host page's own rule), and the
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
	// With the checks defined in the host's agent.yaml (backlog 3.5a):
	// their tuples are judged like any other.
	set, err := a.checks.ResolveHostWithLocal(ctx, orgID, hostID)
	if err != nil {
		return rules.CheckContext{}, err
	}
	// Every resolved check, synthetic and local ones included: the
	// manager's probes (backlog 3.13) and the checks defined in the host's
	// agent.yaml (backlog 3.5a) write into the same tuples an agent's
	// pushed checks do.
	all := set.All()
	intervals := make(map[string]time.Duration, len(all))
	asks := make(map[string]func(item string) bool, len(all))
	asksMetric := make(map[string]func(metric string) bool, len(all))
	for _, c := range all {
		intervals[c.Key] = c.Interval
		asks[c.Key] = asksFor(c)
		asksMetric[c.Key] = c.AsksForMetric
	}
	// The manager's probes are marked: their readings never come in the
	// agent's replay, so they neither end nor wait for one (backlog 3.118).
	synthetic := make(map[string]bool, len(set.Synthetic))
	for _, c := range set.Synthetic {
		synthetic[c.Key] = true
	}
	groups, err := a.checks.GroupsForHost(ctx, orgID, hostID)
	if err != nil {
		return rules.CheckContext{}, err
	}
	ranks := make([]rules.GroupRank, len(groups))
	for i, g := range groups {
		ranks[i] = rules.GroupRank{ID: g.ID, Priority: g.Priority, CreatedAt: g.CreatedAt}
	}
	return rules.CheckContext{Intervals: intervals, Asks: asks, AsksMetric: asksMetric, Synthetic: synthetic, Groups: ranks}, nil
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
