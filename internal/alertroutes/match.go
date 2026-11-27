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
	// Told lists the routes that already queued a message about this
	// subject's alert (their escalation base, alert_escalations position
	// 0). Only an event kind in toldEvents reads it; see MatchRoutes.
	Told []uuid.UUID
}

// MatchRoutes returns every route in routes that applies to s, given which
// monitoring groups the subject's host currently belongs to. A route
// matches when it is enabled, its scope covers the host, and both its
// severity set and its event set contain the subject's values (sets, not
// thresholds — see rules.StateRank's own doc comment for why unknown
// ranking below warn makes a threshold the wrong shape here). A closed or
// acknowledgement event also matches a route outside its severity set
// that already told somebody about the alert (owesTheTold). Routes add up
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
		if !slices.Contains(r.Severities, s.Severity) && !owesTheTold(r, s) {
			continue
		}
		if !slices.Contains(r.Events, s.Event) {
			continue
		}
		matched = append(matched, r)
	}
	return matched
}

// toldEvents are the event kinds owed to the audience of the page rather
// than to whoever the alert's live severity would select: the resolve
// (backlog 2.144) and the acknowledgement and its voiding (2.145, owner
// decision 2026-11-27 — telling the others who were paged is what an
// acknowledgement is for). Snoozes, the opening, a rise and an escalation
// stay on the live severity (the spec's "Resolution" section).
var toldEvents = []alerts.EventKind{alerts.EventClosed, alerts.EventAcknowledged, alerts.EventAckVoided}

// tellsTheTold reports whether kind is owed to the routes that told.
func tellsTheTold(kind alerts.EventKind) bool {
	return slices.Contains(toldEvents, kind)
}

// owesTheTold reports whether r must hear s whatever the alert's severity
// is now: s is a kind in toldEvents and r already queued a message about
// the alert (s.Told). An alert routinely eases before it clears or is
// taken — crit, then warn — and matched on the live severity alone, a
// crit-only route that paged on "opened" would find the event outside its
// set and the person it paged would never hear the alert resolved or that
// somebody has it (backlog 2.144, 2.145). Only the severity selection is
// waived: the route must still be enabled, cover the host and ask for the
// kind; the mute and delivery-window gates still apply
// (resolution.route judges breakthrough at the alert's peak too, and adds
// the channels of the route's escalation steps that fired, backlog 2.146).
func owesTheTold(r Route, s Subject) bool {
	return tellsTheTold(s.Event) && slices.Contains(s.Told, r.ID)
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
