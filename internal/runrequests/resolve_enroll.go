package runrequests

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// agentEnrollment is the picture's AgentEnrollment for a template with
// agent enrollment (spec 2026-11-25 B), from the targets p already
// describes:
//   - a dry run enrols nothing (EnrollNoneDryRun);
//   - an approved request whose approval stored no list (the template did
//     not enrol agents then) enrols nothing (EnrollNoneApprovedBefore);
//   - an approved request shows the list its approval stored, so an agent
//     enrolled after the approval leaves the picture (and the claim
//     guard's fingerprint) as approved; a stored host that is no longer a
//     target shows by id;
//   - anything else shows, live, the targets that have no agent now.
//
// The hosts are ordered by name, then id.
func (r *Resolver) agentEnrollment(ctx context.Context, orgID uuid.UUID, req Request, p Picture) (*PictureAgentEnrollment, error) {
	if p.CheckMode {
		return &PictureAgentEnrollment{Hosts: []string{}, HostIDs: []string{}, NoneReason: EnrollNoneDryRun}, nil
	}
	names := make(map[string]string, len(p.Targets))
	for _, h := range p.Targets {
		names[h.ID] = h.Name
	}
	if req.Status == StatusApproved && req.EnrollHostIDs == nil {
		return &PictureAgentEnrollment{Hosts: []string{}, HostIDs: []string{}, NoneReason: EnrollNoneApprovedBefore}, nil
	}
	var ids []string
	if req.Status == StatusApproved {
		for _, id := range req.EnrollHostIDs {
			ids = append(ids, id.String())
		}
	} else {
		targets := make([]uuid.UUID, 0, len(p.Targets))
		for _, h := range p.Targets {
			id, err := uuid.Parse(h.ID)
			if err != nil {
				return nil, fmt.Errorf("target %q: %w", h.ID, err)
			}
			targets = append(targets, id)
		}
		with, err := r.src.Agents.HostsWithAgent(ctx, orgID, targets)
		if err != nil {
			return nil, fmt.Errorf("look up the targets' agents: %w", err)
		}
		for _, id := range targets {
			if !with[id] {
				ids = append(ids, id.String())
			}
		}
	}
	nameOf := func(id string) string { return cmp.Or(names[id], id) }
	slices.SortFunc(ids, func(a, b string) int { return cmp.Or(cmp.Compare(nameOf(a), nameOf(b)), cmp.Compare(a, b)) })
	e := &PictureAgentEnrollment{Hosts: make([]string, len(ids)), HostIDs: make([]string, len(ids))}
	for i, id := range ids {
		e.Hosts[i], e.HostIDs[i] = nameOf(id), id
	}
	return e, nil
}

// enrollList is the list an approval of p stores: the picture's enrolled
// hosts, nil when the picture enrols nothing because its template does not
// enrol agents.
func enrollList(p Picture) ([]uuid.UUID, error) {
	if p.AgentEnrollment == nil {
		return nil, nil
	}
	out := make([]uuid.UUID, len(p.AgentEnrollment.HostIDs))
	for i, raw := range p.AgentEnrollment.HostIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("enrolled host %q: %w", raw, err)
		}
		out[i] = id
	}
	return out, nil
}

// sameEnrollList reports whether a job's enroll list is the one its
// approval stored: both none, or the same hosts in any order.
func sameEnrollList(job, approved []uuid.UUID) bool {
	if (job == nil) != (approved == nil) {
		return false
	}
	a, b := slices.Clone(job), slices.Clone(approved)
	slices.SortFunc(a, func(x, y uuid.UUID) int { return cmp.Compare(x.String(), y.String()) })
	slices.SortFunc(b, func(x, y uuid.UUID) int { return cmp.Compare(x.String(), y.String()) })
	return slices.Equal(a, b)
}
