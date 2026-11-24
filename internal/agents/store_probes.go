package agents

import (
	"context"
	"fmt"
	"time"

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
		out[i] = ProbeCapable{HostID: r.HostID, HeartbeatInterval: intervalOf(r.HeartbeatIntervalS)}
		if r.LastSeen.Valid {
			t := r.LastSeen.Time
			out[i].LastSeen = &t
		}
	}
	return out, nil
}

// RecordHeartbeatInterval stores the agent's reported heartbeat interval
// by serial (NULL for nil), org-unscoped like the other stream writes.
func (s *SQLStore) RecordHeartbeatInterval(ctx context.Context, serial string, interval *time.Duration) error {
	var seconds *int32
	if interval != nil {
		v := int32(interval.Seconds())
		seconds = &v
	}
	if err := s.q.SetAgentHeartbeatInterval(ctx, gen.SetAgentHeartbeatIntervalParams{Serial: serial, IntervalSeconds: seconds}); err != nil {
		return fmt.Errorf("set agent heartbeat interval: %w", err)
	}
	return nil
}

// intervalOf reads host_agents.heartbeat_interval_s: NULL is "never
// reported".
func intervalOf(seconds *int32) *time.Duration {
	if seconds == nil {
		return nil
	}
	d := time.Duration(*seconds) * time.Second
	return &d
}
