package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
)

// ErrCoveredZoneGrew is the sentinel of CoveredZoneGrowthError.
var ErrCoveredZoneGrew = errors.New("hosts are not covered by the earlier attempt's run in their zone")

// CoveredZoneGrowthError refuses a run request's replay (backlog 2.142):
// a job of the first attempt covers each of Zones, so the replay creates
// no job there, but the zone now holds hosts that job does not (HostIDs
// are those hosts only: they joined the zone, or the first attempt left
// them out). They would never run, so nothing is enqueued.
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

// CheckRunReplay answers, writing nothing, whether EnqueueRunWith(req)
// would be refused with a CoveredZoneGrowthError now (backlog 2.142,
// review I1): a run request's approval asks before it records its
// decision, so a refused re-approval never stands approved, not even for
// a moment in which a worker could claim the earlier attempt's job. Only
// a request that opts in and whose key already started jobs can be
// refused; a scope that does not resolve now answers nil and is left to
// the enqueue, as the replay itself leaves it. The replay keeps its own
// check: things can change in between.
func (s *Service) CheckRunReplay(ctx context.Context, orgID, templateID uuid.UUID, req RunRequest) error {
	if !req.RefuseCoveredZoneGrowth {
		return nil
	}
	return s.checkReplay(ctx, orgID, req.IdempotencyKey, func() ([]ZoneTargets, error) {
		tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
		if err != nil {
			return nil, err
		}
		scope, err := parseScope(tmpl.Scope)
		if err != nil {
			return nil, err
		}
		gate := effectiveLimit(tmpl.Limit, req.Overrides)
		if req.OnlyHost != nil {
			if req, err = s.narrowToHost(ctx, orgID, gate, req); err != nil {
				return nil, err
			}
		}
		targets, _, err := s.resolveRunTargets(ctx, orgID, scope, gate, req)
		return targets, err
	})
}

// CheckRetryReplay is CheckRunReplay for the keyed retry of the finished
// run id an approved retry request makes (RetryFailedInto).
func (s *Service) CheckRetryReplay(ctx context.Context, orgID, id uuid.UUID, idempotencyKey string) error {
	return s.checkReplay(ctx, orgID, idempotencyKey, func() ([]ZoneTargets, error) {
		j, retry, err := s.retryTarget(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		tmpl, err := s.templates.TemplateLookup(ctx, orgID, *j.TemplateID)
		if err != nil {
			return nil, err
		}
		targets, _, err := s.resolveRunTargets(ctx, orgID, render.Scope{HostIDs: retry}, tmpl.Limit, RunRequest{Trigger: TriggerManual})
		return targets, err
	})
}

// checkReplay is the shared read-only check: the jobs an earlier call
// with key created that cover their zone, against the targets resolve
// gives now.
func (s *Service) checkReplay(ctx context.Context, orgID uuid.UUID, key string, resolve func() ([]ZoneTargets, error)) error {
	if key == "" {
		return nil
	}
	group, err := s.store.StartedRunGroup(ctx, orgID, key)
	if err != nil {
		return fmt.Errorf("look up the run's earlier attempt: %w", err)
	}
	if group == nil {
		return nil
	}
	covering, err := s.coveringJobs(ctx, orgID, *group)
	if err != nil || len(covering) == 0 {
		return err
	}
	targets, err := resolve()
	if err != nil {
		// The enqueue reports it, or replays what exists.
		return nil
	}
	return refuseCoveredZoneGrowth(targets, covering)
}
