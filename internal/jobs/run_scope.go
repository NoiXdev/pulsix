// The scope side of a template run: the effective limit, the narrowing to
// one host, the request-time scope check and the host look-ahead of
// schedules.
package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// narrowToHost checks req.OnlyHost before anything is enqueued and returns
// the request with a copy of its overrides whose limit is narrowed to the
// host (narrowLimit). The host must exist and be managed — an unmanaged
// host cannot run anything, so like the ad-hoc route it is ErrNotFound —
// and a limit the matcher can decide must match its name. Whether the
// scope selects it is resolveRunTargets' check (onlyHost). The scope
// resolution keeps gating on gate itself: the narrowed limit carries
// Ansible's '&', which the matcher cannot decide.
func (s *Service) narrowToHost(ctx context.Context, orgID uuid.UUID, gate string, req RunRequest) (RunRequest, error) {
	h, err := s.hosts.GetHost(ctx, orgID, *req.OnlyHost)
	if err != nil {
		if errors.Is(err, hosts.ErrNotFound) {
			err = ErrNotFound
		}
		return req, fmt.Errorf("host %s: %w", *req.OnlyHost, err)
	}
	if !h.Managed {
		return req, fmt.Errorf("%w: host %s is not managed", ErrNotFound, h.ID)
	}
	if matched, decided := parseLimit(gate).matches(h.Name); decided && !matched {
		return req, ErrHostNotInScope
	}
	var ov RunOverrides
	if req.Overrides != nil {
		ov = *req.Overrides
	}
	limit := narrowLimit(gate, h.Name)
	ov.Limit = &limit
	req.Overrides = &ov
	return req, nil
}

// effectiveLimit is the limit the scope resolution gates zones on: the run
// request's own limit replaces the template's, exactly the precedence the
// claim applies when it hands the limit to ansible — so the enqueue never
// creates a job whose zone ansible would empty on a different pattern
// (backlog 2.45).
func effectiveLimit(tmplLimit string, ov *RunOverrides) string {
	if ov != nil && ov.Limit != nil {
		return *ov.Limit
	}
	return tmplLimit
}

// CheckRunScope is EnqueueRunWith's scope checks of a manual run without
// the enqueue: the template's scope resolved under the template's own limit
// (ErrEmptyScope when nothing is left) and, for onlyHost, the narrowing to
// that host. A run request is checked with it when it is made (backlog
// 2.120); the approval enqueues through EnqueueRunWith, which checks again.
// An unknown or unmanaged onlyHost is ErrHostNotInScope here, not
// ErrNotFound: the request names a host the run would not reach. The guards
// that depend on the moment of the run (changed host keys, unpinned hosts)
// are left to the enqueue.
func (s *Service) CheckRunScope(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID) error {
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
	if err != nil {
		return err
	}
	scope, err := parseScope(tmpl.Scope)
	if err != nil {
		return err
	}
	gate := effectiveLimit(tmpl.Limit, nil)
	req := RunRequest{OnlyHost: onlyHost}
	if onlyHost != nil {
		if req, err = s.narrowToHost(ctx, orgID, gate, req); err != nil {
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w (%s)", ErrHostNotInScope, err.Error())
			}
			return err
		}
	}
	_, err = s.scopeTargets(ctx, orgID, scope, gate, req)
	return err
}

// RunTargets resolves the template's scope into the managed host ids a
// run started now would target (every zone together), before the
// changed-host-key exclusion and without the template's limit. The
// maintenance guard asks it which hosts a scheduled run could touch, and
// relies on the limit being left out.
func (s *Service) RunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error) {
	return s.runTargets(ctx, orgID, templateID, false)
}

// LimitedRunTargets is RunTargets under the template's own limit: the hosts
// of the zones the limit keeps, which is what a manual run started now gets
// enqueued for (CheckRunScope's resolution). A scope the limit empties is
// an empty list, not ErrEmptyScope. A run request's picture shows these.
func (s *Service) LimitedRunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error) {
	return s.runTargets(ctx, orgID, templateID, true)
}

func (s *Service) runTargets(ctx context.Context, orgID, templateID uuid.UUID, limited bool) ([]uuid.UUID, error) {
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	scope, err := parseScope(tmpl.Scope)
	if err != nil {
		return nil, err
	}
	limit := ""
	if limited {
		limit = effectiveLimit(tmpl.Limit, nil)
	}
	targets, err := s.resolver.Resolve(ctx, orgID, scope, limit)
	if limited && errors.Is(err, ErrEmptyScope) {
		return []uuid.UUID{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []uuid.UUID{}
	for _, t := range targets {
		out = append(out, t.HostIDs...)
	}
	return out, nil
}

// excludeHosts drops the given hosts from per-zone targets; a zone left
// without hosts is dropped too.
func excludeHosts(targets []ZoneTargets, exclude []uuid.UUID) []ZoneTargets {
	if len(exclude) == 0 {
		return targets
	}
	drop := make(map[uuid.UUID]bool, len(exclude))
	for _, id := range exclude {
		drop[id] = true
	}
	out := make([]ZoneTargets, 0, len(targets))
	for _, t := range targets {
		kept := t.HostIDs[:0:0]
		for _, id := range t.HostIDs {
			if !drop[id] {
				kept = append(kept, id)
			}
		}
		if len(kept) > 0 {
			out = append(out, ZoneTargets{ZoneID: t.ZoneID, HostIDs: kept})
		}
	}
	return out
}
