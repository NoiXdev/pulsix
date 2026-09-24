// Ports the agent gRPC surface (internal/agentapi) needs from app-owned
// services: the host provisioning half of the agents claim (existence
// check, creation inside the token's zone, the monitored flip) — the same
// narrow adapter shape the other gRPC ports use.
package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/hosts"
)

// agentHostProvisioner adapts hosts.Service to agents.HostProvisioner.
// The claim calls all three with its transaction-bound context, so a
// created host and the monitored flip join the claim's atomicity.
type agentHostProvisioner struct{ svc *hosts.Service }

// NewAgentHostProvisioner wires the adapter (also used by
// internal/api/apitest, which builds the agent registry the same way
// Build does so the API tests run against the real wiring).
func NewAgentHostProvisioner(svc *hosts.Service) agents.HostProvisioner {
	return agentHostProvisioner{svc: svc}
}

// GetHost implements agents.HostProvisioner.
func (p agentHostProvisioner) GetHost(ctx context.Context, orgID, hostID uuid.UUID) error {
	_, err := p.svc.GetHost(ctx, orgID, hostID)
	return err
}

// CreateHost implements agents.HostProvisioner. The enrolled host starts
// unmanaged and unmonitored; SetMonitored flips the capability.
func (p agentHostProvisioner) CreateHost(ctx context.Context, orgID, zoneID uuid.UUID, name string) (uuid.UUID, error) {
	h, err := p.svc.CreateHost(ctx, orgID, hosts.HostInput{Name: name, ZoneID: zoneID})
	if err != nil {
		return uuid.Nil, err
	}
	return h.ID, nil
}

// SetMonitored implements agents.HostProvisioner: enrollment is what
// makes a host monitored. managed is left untouched (a pre-created host
// may already be managed).
func (p agentHostProvisioner) SetMonitored(ctx context.Context, orgID, hostID uuid.UUID) error {
	monitored := true
	_, err := p.svc.SetCapabilities(ctx, orgID, hostID, nil, &monitored)
	return err
}
