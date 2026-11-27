package alertroutes_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

func TestMatchRoutes(t *testing.T) {
	host, other := uuid.New(), uuid.New()
	grp, foreignGrp := uuid.New(), uuid.New()
	chA, chB := uuid.New(), uuid.New()

	route := func(name string, enabled bool, sc alertroutes.Scope, id *uuid.UUID,
		sev []rules.State, ev []alerts.EventKind, ch []uuid.UUID) alertroutes.Route {
		return alertroutes.Route{
			ID: uuid.New(), Name: name, Enabled: enabled, Scope: sc, ScopeID: id,
			Severities: sev, Events: ev, Channels: ch,
		}
	}
	crit := []rules.State{rules.StateCrit}
	opened := []alerts.EventKind{alerts.EventOpened}

	tests := []struct {
		name    string
		subject alertroutes.Subject
		groups  []uuid.UUID
		routes  []alertroutes.Route
		want    int // matching routes
	}{
		{
			name:    "an org route covers every host",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			routes:  []alertroutes.Route{route("all", true, alertroutes.ScopeOrg, nil, crit, opened, []uuid.UUID{chA})},
			want:    1,
		},
		{
			// Review Focus 2.
			name:    "a disabled route never matches",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			routes:  []alertroutes.Route{route("off", false, alertroutes.ScopeOrg, nil, crit, opened, []uuid.UUID{chA})},
			want:    0,
		},
		{
			// Review Focus 1. The host is not in that group, so the route
			// must not reach it even though scope and selections agree.
			name:    "a route scoped to a group the host is not in",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			groups:  []uuid.UUID{grp},
			routes:  []alertroutes.Route{route("foreign", true, alertroutes.ScopeMonitoringGroup, &foreignGrp, crit, opened, []uuid.UUID{chA})},
			want:    0,
		},
		{
			name:    "a route scoped to a group the host is in",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			groups:  []uuid.UUID{grp},
			routes:  []alertroutes.Route{route("db", true, alertroutes.ScopeMonitoringGroup, &grp, crit, opened, []uuid.UUID{chA})},
			want:    1,
		},
		{
			name:    "a host route matches only its own host",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			routes:  []alertroutes.Route{route("other", true, alertroutes.ScopeHost, &other, crit, opened, []uuid.UUID{chA})},
			want:    0,
		},
		{
			name:    "a host route matches the host it names",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			routes:  []alertroutes.Route{route("this one", true, alertroutes.ScopeHost, &host, crit, opened, []uuid.UUID{chA})},
			want:    1,
		},
		{
			// unknown ranks BELOW warn (rules.StateRank), which is why the
			// selection is a set: a route that wants crit and unknown but
			// not warn is expressible, and one that wants crit alone does
			// not quietly pick up unknown.
			name:    "a severity outside the set",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateUnknown, Event: alerts.EventOpened},
			routes:  []alertroutes.Route{route("crit only", true, alertroutes.ScopeOrg, nil, crit, opened, []uuid.UUID{chA})},
			want:    0,
		},
		{
			name:    "an event kind outside the set",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventSnoozed},
			routes:  []alertroutes.Route{route("opens", true, alertroutes.ScopeOrg, nil, crit, opened, []uuid.UUID{chA})},
			want:    0,
		},
		{
			name:    "routes add up",
			subject: alertroutes.Subject{HostID: host, Severity: rules.StateCrit, Event: alerts.EventOpened},
			groups:  []uuid.UUID{grp},
			routes: []alertroutes.Route{
				route("all", true, alertroutes.ScopeOrg, nil, crit, opened, []uuid.UUID{chA}),
				route("db", true, alertroutes.ScopeMonitoringGroup, &grp, crit, opened, []uuid.UUID{chB}),
			},
			want: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := alertroutes.MatchRoutes(tc.subject, tc.groups, tc.routes)
			if len(got) != tc.want {
				t.Errorf("MatchRoutes() matched %d routes, want %d", len(got), tc.want)
			}
		})
	}
}

// TestMatchRoutesClosedReachesTheRoutesThatTold pins backlog 2.144: a
// closed event reaches every route that already delivered something about
// its alert (Subject.Told), whatever severity the alert eased to before it
// closed. A crit-only route pages on "opened" at crit; the alert falls to
// warn (a falling severity writes no event) and then clears. Matched on
// the live severity alone, the closed event found no route and the person
// who was paged never heard it had resolved. Every other selection still
// holds: the route must be enabled, cover the host and ask for "closed".
func TestMatchRoutesClosedReachesTheRoutesThatTold(t *testing.T) {
	host, other := uuid.New(), uuid.New()
	crit := []rules.State{rules.StateCrit}
	openedClosed := []alerts.EventKind{alerts.EventOpened, alerts.EventClosed}
	route := func(enabled bool, sc alertroutes.Scope, id *uuid.UUID, ev []alerts.EventKind) alertroutes.Route {
		return alertroutes.Route{
			ID: uuid.New(), Enabled: enabled, Scope: sc, ScopeID: id,
			Severities: crit, Events: ev, Channels: []uuid.UUID{uuid.New()},
		}
	}

	tests := []struct {
		name  string
		event alerts.EventKind
		route alertroutes.Route
		told  bool
		want  int
	}{
		{name: "closed at a lower severity reaches a route that told", event: alerts.EventClosed,
			route: route(true, alertroutes.ScopeOrg, nil, openedClosed), told: true, want: 1},
		{name: "closed at a lower severity misses a route that never told", event: alerts.EventClosed,
			route: route(true, alertroutes.ScopeOrg, nil, openedClosed), want: 0},
		{name: "a route that told but does not ask for closed", event: alerts.EventClosed,
			route: route(true, alertroutes.ScopeOrg, nil, []alerts.EventKind{alerts.EventOpened}), told: true, want: 0},
		{name: "a route that told but is disabled now", event: alerts.EventClosed,
			route: route(false, alertroutes.ScopeOrg, nil, openedClosed), told: true, want: 0},
		{name: "a route that told but no longer covers the host", event: alerts.EventClosed,
			route: route(true, alertroutes.ScopeHost, &other, openedClosed), told: true, want: 0},
		// Only the resolve is owed to the audience of the page: any other
		// event still matches on the severity the alert has now.
		{name: "an acknowledgement at a lower severity still needs the severity", event: alerts.EventAcknowledged,
			route: route(true, alertroutes.ScopeOrg, nil, []alerts.EventKind{alerts.EventAcknowledged}), told: true, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := alertroutes.Subject{HostID: host, Severity: rules.StateWarn, Event: tc.event}
			if tc.told {
				s.Told = []uuid.UUID{tc.route.ID}
			}
			if got := alertroutes.MatchRoutes(s, nil, []alertroutes.Route{tc.route}); len(got) != tc.want {
				t.Errorf("MatchRoutes() matched %d routes, want %d", len(got), tc.want)
			}
		})
	}
}

func TestChannelsUnionDeduplicates(t *testing.T) {
	// Decision 2: two routes pointing at one channel means the alert
	// reaches it once. The union happens here, in resolution, which is
	// why the follower can group by channel alone.
	shared, only := uuid.New(), uuid.New()
	matched := []alertroutes.Route{
		{Channels: []uuid.UUID{shared, only}},
		{Channels: []uuid.UUID{shared}},
	}
	got := alertroutes.Channels(matched)
	if len(got) != 2 {
		t.Fatalf("Channels() = %d channels, want 2 (deduplicated)", len(got))
	}
	if !slices.IsSortedFunc(got, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }) {
		t.Error("Channels() must return a stable, sorted order so that a batch's key is reproducible")
	}
}
