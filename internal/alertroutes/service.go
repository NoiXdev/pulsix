// Service is the package's own front door: the five CRUD verbs the API
// handlers call (Create, Update, Get, List, Delete), plus ForHost, the
// per-host union view backlog 4.1b asks for. It adds the one decision none
// of route.go, match.go or store_sql.go owns on their own — minting an id
// and a timestamp for a new or changed route, and turning a unique-name
// collision into ErrNameTaken — and otherwise defers to Validate (route.go)
// for shape and to Store for persistence.
package alertroutes

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RouteStore is the persistence this service needs — exactly the methods
// it calls, not Store's full surface (ListEnabled is the ticker's own,
// a later task, taken on a caller-owned transaction this service never
// opens). Declaring it here, next to Service, is what lets
// TestForHostReturnsTheUnion exercise ForHost against a fake with no
// database.
type RouteStore interface {
	Create(ctx context.Context, orgID, id uuid.UUID, in Input, now time.Time) (Route, error)
	Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input, now time.Time) (Route, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Route, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Route, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// GroupReader answers which monitoring groups a host currently belongs to.
// It asks for exactly that — a host id in, group ids out — rather than for
// internal/checks.Service itself, so that the later ticker task (which
// needs the same host→group membership to run MatchRoutes) can be built
// directly on a GroupReader, without carrying this Service's CRUD surface
// along for the ride. internal/app wires internal/checks.Service behind
// this interface, the same way maintenance_ports.go's groupHosts adapts
// jobs.ScopeResolver to maintenance.GroupHosts.
type GroupReader interface {
	GroupsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]uuid.UUID, error)
}

// Service composes RouteStore and GroupReader. now and newID are both
// injected — never time.Now or uuid.New called directly — so a test can
// pin the id and the timestamp a Create or Update stamps.
type Service struct {
	store  RouteStore
	groups GroupReader
	now    func() time.Time
	newID  func() uuid.UUID
}

// NewService wires store, groups, now and newID.
func NewService(store RouteStore, groups GroupReader, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, groups: groups, now: now, newID: newID}
}

// Create validates in, mints an id and stamps the current instant, then
// persists the route. A unique-violation on (org_id, name) — the store's
// own CreateAlertRoute — surfaces as ErrNameTaken rather than a bare
// wrapped database error, so the API can map it to a 409 the same way
// every other named resource in this project does.
//
// Validate and ValidateSteps are two separate calls, not one: Validate
// (route.go) covers the route's own four selections and knows nothing about
// Step, while ValidateSteps is a method on Route precisely so a ladder can
// be checked on its own — Task 8's GUI editor previews a candidate ladder
// before the rest of the route is even filled in. Wrapping in.Steps in a
// bare Route{} here costs nothing extra: ValidateSteps reads only its own
// Steps field.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in Input) (Route, error) {
	if err := Validate(in); err != nil {
		return Route{}, err
	}
	if err := (Route{Steps: in.Steps}).ValidateSteps(); err != nil {
		return Route{}, err
	}
	r, err := s.store.Create(ctx, orgID, s.newID(), in, s.now())
	if err != nil {
		return Route{}, fmt.Errorf("create route: %w", err)
	}
	return r, nil
}

// Update validates in and replaces id's row when revision is still current
// (Store.Update's own optimistic lock — ErrRevisionMismatch or ErrNotFound
// otherwise). Same two-call split as Create, and for the same reason.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input) (Route, error) {
	if err := Validate(in); err != nil {
		return Route{}, err
	}
	if err := (Route{Steps: in.Steps}).ValidateSteps(); err != nil {
		return Route{}, err
	}
	r, err := s.store.Update(ctx, orgID, id, revision, in, s.now())
	if err != nil {
		return Route{}, fmt.Errorf("update route: %w", err)
	}
	return r, nil
}

// Get returns one route.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Route, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns every one of org's routes.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Route, error) {
	return s.store.List(ctx, orgID)
}

// Delete removes a route.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// ForHost answers "which routes could reach this host at all" — every
// enabled route whose scope covers hostID, regardless of what its severity
// and event selections are. That is deliberate, not an oversight: a route
// that covers the host but only fires on crit still belongs in this view,
// with its own selections shown beside it in the GUI, because this is the
// only answer an operator has to "why did no mail arrive?" (every route
// filtered itself out) or "why did three?" (three routes cover this host
// at once) — backlog 4.1b's route-union view. MatchRoutes (match.go) is
// the narrower question, "did this one alert actually route", and is not
// reused here for exactly that reason: it would also drop every route
// whose selections do not happen to match, hiding the "only fires on
// crit" routes this view exists to surface.
func (s *Service) ForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]Route, error) {
	routes, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("for host %s: list routes: %w", hostID, err)
	}
	hostGroups, err := s.groups.GroupsForHost(ctx, orgID, hostID)
	if err != nil {
		return nil, fmt.Errorf("for host %s: groups: %w", hostID, err)
	}
	var out []Route
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		if scopeCovers(r, hostID, hostGroups) {
			out = append(out, r)
		}
	}
	return out, nil
}
