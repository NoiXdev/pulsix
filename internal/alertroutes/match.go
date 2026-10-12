package alertroutes

import (
	"bytes"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// Subject is one alert event as the resolver sees it: the host it is about,
// the severity it carries, and which lifecycle event (alerts.EventKind)
// raised it. It is deliberately narrower than alerts.Alert — the resolver
// has no business with an alert's id, its acknowledgement, or its history,
// only with what a route's three selections are compared against.
type Subject struct {
	HostID   uuid.UUID
	Severity rules.State
	Event    alerts.EventKind
}

// MatchRoutes returns every route in routes that applies to s, given which
// monitoring groups the subject's host currently belongs to. A route
// matches when it is enabled, its scope covers the host, and both its
// severity set and its event set contain the subject's values (sets, not
// thresholds — see rules.StateRank's own doc comment for why unknown
// ranking below warn makes a threshold the wrong shape here). Routes add up
// (Scope's own doc comment, backlog 4.1b): every matching route is
// returned, not just the first or the narrowest.
//
// hostGroups must list every monitoring group the host is a member of,
// including an unmanaged host — the same ruling backlog 4.30 already
// settled for alerts' own suppression CTE, ListMaintenanceWindowsForHost
// (queries/alerts.sql), which covers every member of a group because an
// unmanaged host still reports checks and still raises alerts. This is
// deliberately NOT the definition maintenance.HostsInMaintenance uses,
// which covers only a group's managed hosts because an Ansible run that
// cannot reach a host has no reason to be guarded on its account. A caller
// assembling hostGroups from an inventory group's managed membership alone
// would silently stop routing alerts for every unmanaged host in it.
func MatchRoutes(s Subject, hostGroups []uuid.UUID, routes []Route) []Route {
	var matched []Route
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		if !scopeCovers(r, s.HostID, hostGroups) {
			continue
		}
		if !slices.Contains(r.Severities, s.Severity) {
			continue
		}
		if !slices.Contains(r.Events, s.Event) {
			continue
		}
		matched = append(matched, r)
	}
	return matched
}

// scopeCovers reports whether r's scope reaches hostID, given the
// monitoring groups hostID belongs to (see MatchRoutes' own doc comment for
// what that set must include).
func scopeCovers(r Route, hostID uuid.UUID, hostGroups []uuid.UUID) bool {
	switch r.Scope {
	case ScopeOrg:
		return true
	case ScopeMonitoringGroup:
		return r.ScopeID != nil && slices.Contains(hostGroups, *r.ScopeID)
	case ScopeHost:
		return r.ScopeID != nil && *r.ScopeID == hostID
	default:
		return false
	}
}

// Channels returns the union of every channel the matched routes name,
// deduplicated and sorted by its raw bytes. The sort makes the result
// reproducible across calls with the same input regardless of map
// iteration order — the follower (v0.4b's next task) builds a batch key
// from this slice, and a key that moved from one tick to the next for the
// same set of channels would defeat it.
func Channels(matched []Route) []uuid.UUID {
	set := make(map[uuid.UUID]struct{})
	for _, r := range matched {
		for _, ch := range r.Channels {
			set[ch] = struct{}{}
		}
	}
	out := make([]uuid.UUID, 0, len(set))
	for ch := range set {
		out = append(out, ch)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}
