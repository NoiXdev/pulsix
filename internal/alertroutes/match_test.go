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
