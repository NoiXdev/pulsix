package runrequests

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// ErrCoveredZonesGrew is the sentinel of CoveredZonesError.
var ErrCoveredZonesGrew = errors.New("approving again would not run the hosts that joined a zone this request's first approval already enqueued")

// CoveredZonesError refuses a re-approval (backlog 2.142, the owner's
// decision of 2026-11-27): the first approval's enqueue failed partway,
// its jobs in Zones cover those zones, and each zone now holds Hosts that
// none of them does. Approving again would replay those jobs and never
// run the hosts, so nothing is enqueued; whoever needs them asks for a new
// run.
type CoveredZonesError struct {
	Zones []CoveredZone
}

// CoveredZone is a zone a re-approval refused, with the hosts that joined
// it. A name that cannot be read is the id.
type CoveredZone struct {
	ID    uuid.UUID
	Name  string
	Hosts []NamedHost
}

// NamedHost is a host by id and name.
type NamedHost struct {
	ID   uuid.UUID
	Name string
}

func (e *CoveredZonesError) Error() string {
	parts := make([]string, len(e.Zones))
	for i, z := range e.Zones {
		names := make([]string, len(z.Hosts))
		for j, h := range z.Hosts {
			names[j] = h.Name
		}
		parts[i] = fmt.Sprintf("zone %q: %s", z.Name, strings.Join(names, ", "))
	}
	return fmt.Sprintf("%s (%s); nothing was enqueued, request a new run for these hosts", ErrCoveredZonesGrew, strings.Join(parts, "; "))
}

func (e *CoveredZonesError) Unwrap() error { return ErrCoveredZonesGrew }

// namedGrowth turns the queue's refusal into a CoveredZonesError with
// names: the zones from Sources.Zones and the hosts from Sources.Hosts,
// each falling back to its id when it cannot be read (the refusal must
// still say which hosts).
func (r *Resolver) namedGrowth(ctx context.Context, orgID uuid.UUID, grew *jobs.CoveredZoneGrowthError) error {
	zoneNames := map[uuid.UUID]string{}
	if r.src.Zones != nil {
		if zs, err := r.src.Zones.ListZones(ctx, orgID); err == nil {
			for _, z := range zs {
				zoneNames[z.ID] = z.Name
			}
		}
	}
	var ids []uuid.UUID
	for _, z := range grew.Zones {
		ids = append(ids, z.HostIDs...)
	}
	hostNames := map[uuid.UUID]string{}
	if r.src.Hosts != nil {
		if hs, err := r.src.Hosts.ListHostsByIDs(ctx, orgID, ids); err == nil {
			for _, h := range hs {
				hostNames[h.ID] = h.Name
			}
		}
	}
	out := &CoveredZonesError{Zones: make([]CoveredZone, len(grew.Zones))}
	for i, z := range grew.Zones {
		cz := CoveredZone{ID: z.ZoneID, Name: orID(zoneNames, z.ZoneID), Hosts: make([]NamedHost, len(z.HostIDs))}
		for j, h := range z.HostIDs {
			cz.Hosts[j] = NamedHost{ID: h, Name: orID(hostNames, h)}
		}
		out.Zones[i] = cz
	}
	return out
}

func orID(names map[uuid.UUID]string, id uuid.UUID) string {
	if n := names[id]; n != "" {
		return n
	}
	return id.String()
}
