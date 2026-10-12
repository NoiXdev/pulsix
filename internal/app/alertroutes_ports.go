package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/checks"
)

// checkGroupReader is the one method of checks.Service the alert
// follower's group lookup needs. Declared as an interface so the adapter
// below can be tested without a database, the same shape
// maintenance_ports.go and rules_ports.go already use for their own ports.
type checkGroupReader interface {
	GroupsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]checks.Group, error)
}

// alertRouteGroups adapts checks.Service to alertroutes.GroupReader: that
// package names group ids only (its own GroupReader doc comment explains
// why it asks for the narrow answer rather than for checks.Service), and
// never imports internal/checks — the same edge rulesHostChecks draws for
// internal/rules, and in the same place, the wiring.
type alertRouteGroups struct{ checks checkGroupReader }

// NewAlertRouteGroups wires the adapter Build uses.
func NewAlertRouteGroups(svc *checks.Service) alertroutes.GroupReader {
	return alertRouteGroups{checks: svc}
}

// GroupsForHost implements alertroutes.GroupReader: the monitoring groups
// whose selector currently matches the host.
//
// A monitoring group's membership is a selector over the organization's
// hosts, so this covers EVERY member, unmanaged hosts included — which is
// exactly what MatchRoutes' own doc comment requires of this argument
// (backlog 4.30: an unmanaged host still reports checks and still raises
// alerts). The managed-only definition maintenance.HostsInMaintenance
// uses would silently stop routing alerts for every unmanaged host in a
// group-scoped route, which is why the follower is wired to this method
// rather than to a scope resolver like groupHosts (maintenance_ports.go).
//
// The error is not softened into an empty answer: a resolver outage
// mistaken for "this host is in no group" would quietly drop every
// group-scoped route's alerts, and a tick that fails instead leaves every
// event undecided for the next one to find.
func (a alertRouteGroups) GroupsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]uuid.UUID, error) {
	groups, err := a.checks.GroupsForHost(ctx, orgID, hostID)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, len(groups))
	for i, g := range groups {
		out[i] = g.ID
	}
	return out, nil
}
