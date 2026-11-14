package checks

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// LocalCheck is one check defined in a host's agent.yaml (backlog 3.5a), as
// the host's agent last reported it in its heartbeat: already validated by
// the manager when stored (a key starting with checkplugins.LocalCheckPrefix,
// an agent-run plugin, params that plugin accepts, an interval within
// MinInterval and MaxInterval).
type LocalCheck struct {
	Key, Plugin string
	Params      map[string]any
	Interval    time.Duration
}

// LocalCheckSource is the port to what hosts' agents reported about their
// local checks. Declared here, next to the resolver that reads it, and
// satisfied in internal/app by the agents service. A host without an agent,
// or whose agent's report is unknown (an older agent, no heartbeat yet),
// has no local checks; an error is a failed read, never "none".
//
// One host at a time: only ResolveHostWithLocal reads it, for the callers
// that judge or show a host's tuples (the rule evaluation, the host page,
// the effective-rules view). The batch resolves feed the push and the
// probes, which never use Local.
type LocalCheckSource interface {
	// LocalChecksByHost is hostID's local checks.
	LocalChecksByHost(ctx context.Context, orgID, hostID uuid.UUID) ([]LocalCheck, error)
}

// WithLocalChecks wires the local checks port; a Service built without it
// resolves every host's Local to an empty list.
func WithLocalChecks(src LocalCheckSource) Option { return func(s *Service) { s.localChecks = src } }

// localForHost reads hostID's local checks, or none without the port.
func (s *Service) localForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]LocalCheck, error) {
	if s.localChecks == nil {
		return nil, nil
	}
	local, err := s.localChecks.LocalChecksByHost(ctx, orgID, hostID)
	if err != nil {
		return nil, fmt.Errorf("resolve: local checks: %w", err)
	}
	return local, nil
}

// resolveLocal turns a host's local checks into ResolvedSet.Local: bound at
// BindingLocal with no source id, sorted by key, never nil. A key the
// manager resolves itself (in checks or synthetic) is left out — the
// manager's check wins, as it does on the agent, which runs the manager's
// and not its own under a shared key.
func resolveLocal(local []LocalCheck, checks, synthetic []Resolved) []Resolved {
	taken := make(map[string]bool, len(checks)+len(synthetic))
	for _, r := range checks {
		taken[r.Key] = true
	}
	for _, r := range synthetic {
		taken[r.Key] = true
	}
	out := make([]Resolved, 0, len(local))
	for _, c := range local {
		if taken[c.Key] {
			continue
		}
		out = append(out, Resolved{Key: c.Key, Plugin: c.Plugin, Params: c.Params, Interval: c.Interval, Source: BindingLocal})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
