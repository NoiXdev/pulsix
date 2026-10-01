package app

import (
	"context"
	"time"

	"github.com/google/uuid"

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
// check key, and the monitoring groups whose selector currently matches
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
	intervals := make(map[string]time.Duration, len(set.Checks))
	for _, c := range set.Checks {
		intervals[c.Key] = c.Interval
	}
	groups, err := a.checks.GroupsForHost(ctx, orgID, hostID)
	if err != nil {
		return rules.CheckContext{}, err
	}
	ranks := make([]rules.GroupRank, len(groups))
	for i, g := range groups {
		ranks[i] = rules.GroupRank{ID: g.ID, Priority: g.Priority, CreatedAt: g.CreatedAt}
	}
	return rules.CheckContext{Intervals: intervals, Groups: ranks}, nil
}

// ItemCheckKeys adapts rules.Catalogue() into the set internal/items
// validates a decision's CheckKey against (items.NewService's own
// required constructor argument): Catalogue's keys are metric names, so
// this collapses them to the distinct check keys those metrics belong to.
// It is a function value, not a value computed once here, so a catalogue
// entry added later is picked up by the very next validation or
// discovery call rather than requiring a manager restart — the same
// reason items.NewService takes a function rather than a snapshot.
//
// Exported (not itemCheckKeys) so internal/api/apitest can wire the exact
// same check-key set production does, rather than a hand-copied second
// list that could silently drift from it.
func ItemCheckKeys() map[string]struct{} {
	entries := rules.Catalogue()
	keys := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		keys[e.CheckKey] = struct{}{}
	}
	return keys
}
