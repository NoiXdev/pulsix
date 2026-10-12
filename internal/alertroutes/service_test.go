package alertroutes_test

import (
	"context"
	"errors"
	"strings"
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

func TestForHostSkipsDisabledRoutes(t *testing.T) {
	// ForHost's own doc comment: "every ENABLED route whose scope covers
	// hostID" — a disabled route must not appear even though its scope
	// otherwise matches.
	host := uuid.New()
	store := &fakeStore{routes: []alertroutes.Route{
		{ID: uuid.New(), Name: "off", Enabled: false, Scope: alertroutes.ScopeOrg,
			Severities: []rules.State{rules.StateCrit}, Events: []alerts.EventKind{alerts.EventOpened},
			Channels: []uuid.UUID{uuid.New()}},
	}}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)

	got, err := svc.ForHost(context.Background(), uuid.New(), host)
	if err != nil {
		t.Fatalf("ForHost: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ForHost() = %+v, want none — the only route is disabled", got)
	}
}

func TestForHostWrapsAStoreListError(t *testing.T) {
	store := &spyStore{listErr: errors.New("list boom")}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)

	if _, err := svc.ForHost(context.Background(), uuid.New(), uuid.New()); err == nil || !strings.Contains(err.Error(), "list boom") {
		t.Fatalf("ForHost() error = %v, want it to carry the store's own list error", err)
	}
}

// erroringGroups is a GroupReader that always fails, for
// TestForHostWrapsAGroupsError — the one caller-supplied dependency
// fakeGroups cannot exercise an error path for.
type erroringGroups struct{ err error }

func (e erroringGroups) GroupsForHost(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return nil, e.err
}

func TestForHostWrapsAGroupsError(t *testing.T) {
	store := &fakeStore{}
	svc := alertroutes.NewService(store, erroringGroups{err: errors.New("groups boom")}, time.Now, uuid.New)

	if _, err := svc.ForHost(context.Background(), uuid.New(), uuid.New()); err == nil || !strings.Contains(err.Error(), "groups boom") {
		t.Fatalf("ForHost() error = %v, want it to carry the groups error", err)
	}
}

// spyStore implements alertroutes.RouteStore for the Service-level CRUD
// tests below (Create/Update/Get/List/Delete): unlike fakeStore, which
// only ever backs List (through ForHost), every method here records
// whether and how it was called and returns whatever the test wired it
// to — the shape a test needs to tell "Validate rejected this before the
// store was ever reached" from "the store was reached with the right
// arguments".
type spyStore struct {
	createCalled          bool
	createOrgID, createID uuid.UUID
	createNow             time.Time
	createOut             alertroutes.Route
	createErr             error
	updateCalled          bool
	updateOrgID, updateID uuid.UUID
	updateRev             int64
	updateErr             error
	getCalled             bool
	getOrgID, getID       uuid.UUID
	getOut                alertroutes.Route
	getErr                error
	listCalled            bool
	listOrgID             uuid.UUID
	listOut               []alertroutes.Route
	listErr               error
	deleteCalled          bool
	deleteOrgID, deleteID uuid.UUID
	deleteErr             error
}

func (s *spyStore) Create(_ context.Context, orgID, id uuid.UUID, _ alertroutes.Input, now time.Time) (alertroutes.Route, error) {
	s.createCalled = true
	s.createOrgID, s.createID, s.createNow = orgID, id, now
	return s.createOut, s.createErr
}

func (s *spyStore) Update(_ context.Context, orgID, id uuid.UUID, revision int64, _ alertroutes.Input, _ time.Time) (alertroutes.Route, error) {
	s.updateCalled = true
	s.updateOrgID, s.updateID, s.updateRev = orgID, id, revision
	return alertroutes.Route{}, s.updateErr
}

func (s *spyStore) Get(_ context.Context, orgID, id uuid.UUID) (alertroutes.Route, error) {
	s.getCalled = true
	s.getOrgID, s.getID = orgID, id
	return s.getOut, s.getErr
}

func (s *spyStore) List(_ context.Context, orgID uuid.UUID) ([]alertroutes.Route, error) {
	s.listCalled = true
	s.listOrgID = orgID
	return s.listOut, s.listErr
}

func (s *spyStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	s.deleteCalled = true
	s.deleteOrgID, s.deleteID = orgID, id
	return s.deleteErr
}

// validRouteInput is the minimal Input Validate accepts — every test below
// that exercises a store call rather than Validate's own rejection starts
// from it, modifying only what that test is about.
func validRouteInput(ch uuid.UUID) alertroutes.Input {
	return alertroutes.Input{
		Name: "all", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{ch},
	}
}

func TestServiceCreateRejectsInvalidInputWithoutTouchingTheStore(t *testing.T) {
	store := &spyStore{}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)

	if _, err := svc.Create(context.Background(), uuid.New(), alertroutes.Input{}); !errors.Is(err, alertroutes.ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
	if store.createCalled {
		t.Fatal("Create reached the store despite Validate rejecting the input")
	}
}

func TestServiceCreateMintsIDAndStampsNow(t *testing.T) {
	fixedID := uuid.New()
	fixedNow := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	store := &spyStore{createOut: alertroutes.Route{Name: "all"}}
	svc := alertroutes.NewService(store, &fakeGroups{}, func() time.Time { return fixedNow }, func() uuid.UUID { return fixedID })
	orgID, ch := uuid.New(), uuid.New()

	got, err := svc.Create(context.Background(), orgID, validRouteInput(ch))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !store.createCalled || store.createOrgID != orgID || store.createID != fixedID || !store.createNow.Equal(fixedNow) {
		t.Fatalf("store.Create called with org=%v id=%v now=%v, want org=%v id=%v now=%v",
			store.createOrgID, store.createID, store.createNow, orgID, fixedID, fixedNow)
	}
	if got.Name != "all" {
		t.Fatalf("Create() = %+v, want the store's own return value passed through unchanged", got)
	}
}

func TestServiceCreateWrapsAStoreErrorRatherThanSwallowingIt(t *testing.T) {
	store := &spyStore{createErr: alertroutes.ErrNameTaken}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)

	if _, err := svc.Create(context.Background(), uuid.New(), validRouteInput(uuid.New())); !errors.Is(err, alertroutes.ErrNameTaken) {
		t.Fatalf("Create() error = %v, want it to still carry ErrNameTaken", err)
	}
}

func TestServiceUpdateRejectsInvalidInputWithoutTouchingTheStore(t *testing.T) {
	store := &spyStore{}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)

	if _, err := svc.Update(context.Background(), uuid.New(), uuid.New(), 1, alertroutes.Input{}); !errors.Is(err, alertroutes.ErrInvalidInput) {
		t.Fatalf("Update() error = %v, want ErrInvalidInput", err)
	}
	if store.updateCalled {
		t.Fatal("Update reached the store despite Validate rejecting the input")
	}
}

func TestServiceUpdatePassesRevisionAndWrapsAStoreError(t *testing.T) {
	store := &spyStore{updateErr: alertroutes.ErrRevisionMismatch}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)
	orgID, id := uuid.New(), uuid.New()

	if _, err := svc.Update(context.Background(), orgID, id, 7, validRouteInput(uuid.New())); !errors.Is(err, alertroutes.ErrRevisionMismatch) {
		t.Fatalf("Update() error = %v, want it to still carry ErrRevisionMismatch", err)
	}
	if !store.updateCalled || store.updateOrgID != orgID || store.updateID != id || store.updateRev != 7 {
		t.Fatalf("store.Update called with org=%v id=%v rev=%v, want org=%v id=%v rev=7",
			store.updateOrgID, store.updateID, store.updateRev, orgID, id)
	}
}

func TestServiceGetListDeleteDelegateToTheStore(t *testing.T) {
	wantRoute := alertroutes.Route{Name: "all"}
	store := &spyStore{getOut: wantRoute, listOut: []alertroutes.Route{wantRoute}}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)
	orgID, id := uuid.New(), uuid.New()

	gotRoute, err := svc.Get(context.Background(), orgID, id)
	if err != nil || gotRoute.Name != "all" || !store.getCalled || store.getOrgID != orgID || store.getID != id {
		t.Fatalf("Get() = %+v, %v; store called=%v org=%v id=%v", gotRoute, err, store.getCalled, store.getOrgID, store.getID)
	}

	gotList, err := svc.List(context.Background(), orgID)
	if err != nil || len(gotList) != 1 || !store.listCalled || store.listOrgID != orgID {
		t.Fatalf("List() = %+v, %v; store called=%v org=%v", gotList, err, store.listCalled, store.listOrgID)
	}

	if err := svc.Delete(context.Background(), orgID, id); err != nil || !store.deleteCalled || store.deleteOrgID != orgID || store.deleteID != id {
		t.Fatalf("Delete() = %v; store called=%v org=%v id=%v", err, store.deleteCalled, store.deleteOrgID, store.deleteID)
	}
}

func TestServiceGetAndListPassAStoreErrorThroughUnwrapped(t *testing.T) {
	boom := errors.New("boom")
	store := &spyStore{getErr: alertroutes.ErrNotFound, listErr: boom}
	svc := alertroutes.NewService(store, &fakeGroups{}, time.Now, uuid.New)

	if _, err := svc.Get(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, alertroutes.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound unwrapped", err)
	}
	if _, err := svc.List(context.Background(), uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("List() error = %v, want the store's own error untouched", err)
	}
}
