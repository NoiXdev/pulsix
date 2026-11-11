package probes

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
)

// Orgs lists every organization: the runner's iteration root.
// hosts.Service's ListOrgIDs.
type Orgs interface {
	ListOrgIDs(ctx context.Context) ([]uuid.UUID, error)
}

// Resolver resolves every monitored host of an organization:
// checks.Service's ResolveOrg. The runner probes each set's Synthetic
// checks.
type Resolver interface {
	ResolveOrg(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error)
}

// Hosts reads the hosts the runner probes, for their address and
// monitored flag: hosts.Service's ListHostsByIDs.
type Hosts interface {
	ListHostsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error)
}

// Recorder stores a probe's results like an agent's: checks.Service's
// RecordObservations.
type Recorder interface {
	RecordObservations(ctx context.Context, orgID uuid.UUID, obs []checks.Observation) (checks.Ingest, error)
}

// Locker is the advisory lock that makes one manager the prober (the
// schedule.Locker shape; production passes schedule.NewAdvisoryLocker
// with AdvisoryKeyProbes).
type Locker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// ProbeFunc runs one probe of address and returns its observations. The
// probe must honour ctx's deadline; production is probe.Run with the
// manager's guard.
type ProbeFunc func(ctx context.Context, plugin string, params map[string]any, address string) []probe.Observation
