package probes

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/snmp"
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

// Recorder stores a probe's results like an agent's, except that they
// never measure the host's clock: checks.Service's
// RecordProbeObservations.
type Recorder interface {
	RecordProbeObservations(ctx context.Context, orgID uuid.UUID, obs []checks.Observation) (checks.Ingest, error)
}

// Locker is the advisory lock that makes one manager the prober (the
// schedule.Locker shape; production passes schedule.NewAdvisoryLocker
// with AdvisoryKeyProbes).
type Locker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// ProbeFunc runs one probe of address and returns its observations. secret
// holds the fields of the check's secret, nil for a plugin that takes
// none; it is never merged into params. The probe must honour ctx's
// deadline; production is the runner's all.Registry with the manager's
// guard. Plugins with a Poller (snmp) do not go through it.
type ProbeFunc func(ctx context.Context, plugin string, params map[string]any, address string, secret map[string]string) []probe.Observation

// Poller polls one snmp slot and keeps what its next poll needs (the
// counters its rates are computed from): snmp.Poller with the manager's
// guard. The runner keeps one per slot and never polls one from two
// goroutines at once.
type Poller interface {
	Poll(ctx context.Context, params map[string]any, address string, secret map[string]string) ([]probe.Observation, snmp.Info)
}

// Secrets reads the fields of an organization's stored secret, for the
// probes whose plugin takes one: orgsecrets.Service's Fields, which
// returns orgsecrets.ErrNotFound when the organization has no secret of
// that name.
type Secrets interface {
	Fields(ctx context.Context, orgID uuid.UUID, name string) (map[string]string, error)
}
