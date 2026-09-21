package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// groupHosts adapts the job scope resolver to maintenance.GroupHosts: a
// group window covers the managed hosts a run scoped to that group (and
// its subgroups) would target — the same selection, so the maintenance
// guard and the run agree on which hosts a group means.
type groupHosts struct{ r jobs.ScopeResolver }

// NewGroupHosts wires the adapter (also used by internal/api/apitest).
func NewGroupHosts(r jobs.ScopeResolver) maintenance.GroupHosts { return groupHosts{r: r} }

// HostsInGroup resolves the group. A group deleted meanwhile covers
// nothing (its windows go with it through the foreign key), and neither
// does a group that currently has no managed hosts (jobs.ErrEmptyScope) —
// an empty group is a normal outcome, not a failure, and must not make
// the maintenance guard error out for every other window in the org.
func (g groupHosts) HostsInGroup(ctx context.Context, orgID, groupID uuid.UUID) ([]uuid.UUID, error) {
	targets, err := g.r.Resolve(ctx, orgID, render.Scope{GroupID: &groupID}, "")
	if errors.Is(err, jobs.ErrNotFound) || errors.Is(err, jobs.ErrEmptyScope) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, t := range targets {
		out = append(out, t.HostIDs...)
	}
	return out, nil
}
