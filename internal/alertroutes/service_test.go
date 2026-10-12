package alertroutes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// ptr is a small generic helper for taking the address of a literal, used
// below to build a Route's ScopeID inline.
func ptr[T any](v T) *T { return &v }

// fakeStore implements alertroutes.RouteStore for TestForHostReturnsTheUnion,
// which only exercises List (through Service.ForHost); the remaining
// RouteStore methods return zero values since no other test here calls them.
type fakeStore struct{ routes []alertroutes.Route }

func (f *fakeStore) Create(context.Context, uuid.UUID, uuid.UUID, alertroutes.Input, time.Time) (alertroutes.Route, error) {
	return alertroutes.Route{}, nil
}

func (f *fakeStore) Update(context.Context, uuid.UUID, uuid.UUID, int64, alertroutes.Input, time.Time) (alertroutes.Route, error) {
	return alertroutes.Route{}, nil
}

func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (alertroutes.Route, error) {
	return alertroutes.Route{}, nil
}

func (f *fakeStore) List(context.Context, uuid.UUID) ([]alertroutes.Route, error) {
	return f.routes, nil
}

func (f *fakeStore) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

// fakeGroups implements alertroutes.GroupReader.
type fakeGroups struct{ of map[uuid.UUID][]uuid.UUID }

func (f *fakeGroups) GroupsForHost(_ context.Context, _, host uuid.UUID) ([]uuid.UUID, error) {
	return f.of[host], nil
}

func TestForHostReturnsTheUnion(t *testing.T) {
	// This is the view backlog 4.1b asks for, and the only answer a user
	// has to "why did no mail arrive?" once routes add up.
	host, grp := uuid.New(), uuid.New()
	ch := uuid.New()
	store := &fakeStore{routes: []alertroutes.Route{
		{ID: uuid.New(), Name: "all", Enabled: true, Scope: alertroutes.ScopeOrg,
			Severities: []rules.State{rules.StateCrit}, Events: []alerts.EventKind{alerts.EventOpened},
			Channels: []uuid.UUID{ch}},
		{ID: uuid.New(), Name: "db", Enabled: true, Scope: alertroutes.ScopeMonitoringGroup, ScopeID: &grp,
			Severities: []rules.State{rules.StateCrit}, Events: []alerts.EventKind{alerts.EventOpened},
			Channels: []uuid.UUID{ch}},
		{ID: uuid.New(), Name: "elsewhere", Enabled: true, Scope: alertroutes.ScopeHost, ScopeID: ptr(uuid.New()),
			Severities: []rules.State{rules.StateCrit}, Events: []alerts.EventKind{alerts.EventOpened},
			Channels: []uuid.UUID{ch}},
	}}
	groups := &fakeGroups{of: map[uuid.UUID][]uuid.UUID{host: {grp}}}
	svc := alertroutes.NewService(store, groups, time.Now, uuid.New)

	got, err := svc.ForHost(context.Background(), uuid.New(), host)
	if err != nil {
		t.Fatalf("ForHost: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ForHost() = %d routes, want 2 (the org route and the group route, not the other host's)", len(got))
	}
}
