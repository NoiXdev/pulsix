package agents

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SetProbesSupported stores the heartbeat's probes_supported flag by host,
// org-unscoped like the other heartbeat writes.
func (s *SQLStore) SetProbesSupported(ctx context.Context, hostID uuid.UUID, supported bool) error {
	if err := s.q.SetAgentProbesSupported(ctx, gen.SetAgentProbesSupportedParams{HostID: hostID, Supported: supported}); err != nil {
		return fmt.Errorf("set agent probes supported: %w", err)
	}
	return nil
}

// ProbeCapableAgents lists the organization's agents that report
// probes_supported, under the caller's organization scope.
func (s *SQLStore) ProbeCapableAgents(ctx context.Context, orgID uuid.UUID) ([]ProbeCapable, error) {
	rows, err := s.q.ListProbeCapableAgents(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list probe capable agents: %w", err)
	}
	out := make([]ProbeCapable, len(rows))
	for i, r := range rows {
		out[i] = ProbeCapable{HostID: r.HostID}
		if r.LastSeen.Valid {
			t := r.LastSeen.Time
			out[i].LastSeen = &t
		}
	}
	return out, nil
}
