package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
)

// agentLocalChecks is the slice of agents.Service the adapter below reads:
// each host's agent's latest local checks report (backlog 3.5a).
type agentLocalChecks interface {
	LocalChecksByHost(ctx context.Context, orgID, hostID uuid.UUID) (agents.LocalChecksReport, error)
	LocalChecksByHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]agents.LocalChecksReport, error)
}

// checksLocalChecks adapts agents.Service to checks.LocalCheckSource: the
// checks defined in each host's agent.yaml, as its agent last reported
// them. The edge lives here because internal/checks does not depend on
// internal/agents. An unknown report (an agent older than local checks, no
// heartbeat yet, no agent) is no local checks.
type checksLocalChecks struct{ agents agentLocalChecks }

// NewChecksLocalChecks wires the adapter Build uses; exported, like
// NewChecksHostMatcher, so internal/api/apitest wires the resolver the
// same way.
func NewChecksLocalChecks(svc *agents.Service) checks.LocalCheckSource {
	return checksLocalChecks{agents: svc}
}

// LocalChecksByHost implements checks.LocalCheckSource.
func (a checksLocalChecks) LocalChecksByHost(ctx context.Context, orgID, hostID uuid.UUID) ([]checks.LocalCheck, error) {
	report, err := a.agents.LocalChecksByHost(ctx, orgID, hostID)
	if err != nil {
		return nil, err
	}
	return toChecksLocal(report), nil
}

// LocalChecksByHosts implements checks.LocalCheckSource; a host without
// local checks is absent from the map.
func (a checksLocalChecks) LocalChecksByHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID][]checks.LocalCheck, error) {
	reports, err := a.agents.LocalChecksByHosts(ctx, orgID, hostIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]checks.LocalCheck, len(reports))
	for id, report := range reports {
		if local := toChecksLocal(report); len(local) > 0 {
			out[id] = local
		}
	}
	return out, nil
}

// toChecksLocal is a report's checks as the resolver's type; nil for an
// unknown report or one without checks.
func toChecksLocal(report agents.LocalChecksReport) []checks.LocalCheck {
	if !report.Known || len(report.Checks) == 0 {
		return nil
	}
	out := make([]checks.LocalCheck, len(report.Checks))
	for i, c := range report.Checks {
		out[i] = checks.LocalCheck{Key: c.Key, Plugin: c.Plugin, Params: c.Params, Interval: c.Interval}
	}
	return out
}
