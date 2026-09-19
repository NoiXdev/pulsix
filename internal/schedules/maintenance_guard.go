package schedules

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// MaintenanceChecker answers which hosts are in a maintenance window at an
// instant (maintenance.Service).
type MaintenanceChecker interface {
	HostsInMaintenance(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID, at time.Time) (map[uuid.UUID]bool, error)
}

// SetMaintenance injects the maintenance-window checker the maintenance
// guard asks — post-construction wiring like the job queue's sinks, so the
// schedules service keeps one constructor for its many callers.
func (s *Service) SetMaintenance(m MaintenanceChecker) { s.maint = m }

// errNoMaintenanceChecker is a wiring gap: a schedule asks for a
// maintenance guard but nothing answers it.
var errNoMaintenanceChecker = errors.New("no maintenance checker is wired")

// hostsInMaintenance returns the run's targeted hosts that are in a
// maintenance window at now, and whether that is every targeted host.
// targets is the template's resolved scope (RunTargets), not narrowed by
// the schedule's own limit override — the guard can flag a host the
// limit would never have touched, which is fail-safe (it never
// under-guards), not a bug.
func (s *Service) hostsInMaintenance(ctx context.Context, sch Schedule, now time.Time) ([]uuid.UUID, bool, error) {
	if s.maint == nil {
		return nil, false, errNoMaintenanceChecker
	}
	scoped := db.WithOrgScope(ctx, sch.OrgID)
	targets, err := s.runs.RunTargets(scoped, sch.OrgID, sch.TemplateID)
	if err != nil {
		return nil, false, fmt.Errorf("resolve run targets: %w", err)
	}
	covered, err := s.maint.HostsInMaintenance(scoped, sch.OrgID, targets, now)
	if err != nil {
		return nil, false, fmt.Errorf("check maintenance windows: %w", err)
	}
	var in []uuid.UUID
	for _, h := range targets {
		if covered[h] {
			in = append(in, h)
		}
	}
	return in, len(targets) > 0 && len(in) == len(targets), nil
}

// joinReason appends b to a reason that may already say something.
func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
