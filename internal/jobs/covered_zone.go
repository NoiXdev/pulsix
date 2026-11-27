package jobs

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// ErrCoveredZoneGrew is the sentinel of CoveredZoneGrowthError.
var ErrCoveredZoneGrew = errors.New("hosts joined a zone the run's first attempt already covers")

// CoveredZoneGrowthError refuses a run request's replay (backlog 2.142):
// a job of the first attempt covers each of Zones, so the replay creates
// no job there, but the zone now holds hosts that job does not (HostIDs
// are those hosts only). They would never run, so nothing is enqueued.
type CoveredZoneGrowthError struct {
	Zones []ZoneTargets
}

func (e *CoveredZoneGrowthError) Error() string {
	parts := make([]string, len(e.Zones))
	for i, z := range e.Zones {
		ids := make([]string, len(z.HostIDs))
		for j, h := range z.HostIDs {
			ids[j] = h.String()
		}
		parts[i] = fmt.Sprintf("zone %s: %s", z.ZoneID, strings.Join(ids, ", "))
	}
	return fmt.Sprintf("%s (%s)", ErrCoveredZoneGrew, strings.Join(parts, "; "))
}

func (e *CoveredZoneGrowthError) Unwrap() error { return ErrCoveredZoneGrew }

// refuseCoveredZoneGrowth checks a replay's resolved targets against the
// jobs of the earlier attempt that cover their zones (alreadyDone): a
// target zone holding a host none of those jobs holds is a
// CoveredZoneGrowthError. Zones nothing covers, and covered zones with
// fewer hosts than their jobs, pass.
func refuseCoveredZoneGrowth(targets []ZoneTargets, alreadyDone []Job) error {
	covered := make(map[uuid.UUID]map[uuid.UUID]bool, len(alreadyDone))
	for _, j := range alreadyDone {
		if covered[j.ZoneID] == nil {
			covered[j.ZoneID] = map[uuid.UUID]bool{}
		}
		for _, h := range j.HostIDs {
			covered[j.ZoneID][h] = true
		}
	}
	var grown []ZoneTargets
	for _, t := range targets {
		hosts, ok := covered[t.ZoneID]
		if !ok {
			continue
		}
		var joined []uuid.UUID
		for _, h := range t.HostIDs {
			if !hosts[h] && !slices.Contains(joined, h) {
				joined = append(joined, h)
			}
		}
		if len(joined) > 0 {
			grown = append(grown, ZoneTargets{ZoneID: t.ZoneID, HostIDs: joined})
		}
	}
	if len(grown) > 0 {
		return &CoveredZoneGrowthError{Zones: grown}
	}
	return nil
}
