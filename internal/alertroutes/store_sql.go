// SQLStore is the only part of this package that touches a database: route
// CRUD against migration 00052's alert_routes table and its three selection
// tables (alert_route_severities, alert_route_events, alert_route_channels).
// Modelled on internal/alerts/store_sql.go throughout — how it reaches
// *db.DB, how it maps "no rows" to ErrNotFound and "zero rows updated after
// a successful read" to ErrRevisionMismatch.
//
// Every read and write below goes through gen.New(s.d) (or gen.New(tx) for
// the caller-owned transaction ListEnabled takes), never s.d.Pool()
// directly: a handler-bound request context carries its organization's
// row-level-security scope (db.WithOrgScope), and reaching for the bare
// pool would silently drop that layer even though the hand-written org_id
// predicate below would still narrow the result — exactly the gap
// v0.4a's final review caught in this package's own model file (that
// file's writeAlertAction doc comment explains why). The explicit
// predicate and the composite foreign keys migration 00052 put on
// alert_route_channels, alert_route_severities and alert_route_events are
// what make TestStoreIsolatesOrganizations and
// TestStoreRejectsAForeignChannel pass under a plain, unscoped context the
// same way internal/alerts' own org-guard tests do; RLS is the second,
// production-only layer on top of it.
package alertroutes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/rules"
)

// Store is the route store. The service (a later task) builds exactly one
// of these on the shared *db.DB, the same shape notify.SQLStore and
// maintenance.SQLStore both already take.
type Store struct{ d *db.DB }

// NewSQLStore builds the store on d.
func NewSQLStore(d *db.DB) *Store { return &Store{d: d} }

// selection is one route's three selections, grouped out of
// ListRouteSelections' own flat, three-nullable-column row shape (that
// query's own doc comment explains why the columns are split rather than
// a single discriminated one).
type selection struct {
	Severities []rules.State
	Events     []alerts.EventKind
	Channels   []uuid.UUID
}

// selectionsByRoute groups every one of org's selections by route id in a
// single round trip — List's own N+1 concern (ListRouteSelections' doc
// comment) — and Get reuses it rather than a second, route-scoped query,
// since no such query exists and one organization's full selection set is
// the same cost ListAlertRoutes already pays for the route rows themselves.
func (s *Store) selectionsByRoute(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (map[uuid.UUID]selection, error) {
	rows, err := q.ListRouteSelections(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list route selections: %w", err)
	}
	out := make(map[uuid.UUID]selection, len(rows))
	for _, r := range rows {
		sel := out[r.RouteID]
		switch {
		case r.Severity != nil:
			sel.Severities = append(sel.Severities, rules.State(*r.Severity))
		case r.EventKind != nil:
			sel.Events = append(sel.Events, alerts.EventKind(*r.EventKind))
		case r.ChannelID.Valid:
			sel.Channels = append(sel.Channels, uuid.UUID(r.ChannelID.Bytes))
		}
		out[r.RouteID] = sel
	}
	return out, nil
}

// scopeColumns maps Input's single optional ScopeID onto the two typed
// columns migration 00052 actually carries, picking the one the scope
// names — scope_group_id for ScopeMonitoringGroup, scope_host_id for
// ScopeHost, neither for ScopeOrg. Validate (route.go) is what guarantees
// ScopeID is non-nil exactly when a scope needs it; this trusts that
// already holds, the same division of labour toColumns/toWindow draw in
// internal/maintenance/store_sql.go for that package's own two-column
// target.
func scopeColumns(scope Scope, id *uuid.UUID) (groupID, hostID pgtype.UUID) {
	switch scope {
	case ScopeMonitoringGroup:
		groupID = db.NullUUID(id)
	case ScopeHost:
		hostID = db.NullUUID(id)
	}
	return groupID, hostID
}

// scopeIDFromColumns reverses scopeColumns when reading a row back: the
// scope column itself says which of the two nullable columns to trust, so
// this never has to guess from "whichever one happens to be set" the way a
// bare coalesce would.
func scopeIDFromColumns(scope string, groupID, hostID pgtype.UUID) *uuid.UUID {
	switch Scope(scope) {
	case ScopeMonitoringGroup:
		return db.UUIDPtr(groupID)
	case ScopeHost:
		return db.UUIDPtr(hostID)
	default:
		return nil
	}
}

// statesToStrings and eventsToStrings convert Input's typed selections to
// the text[] the three ReplaceRoute* queries take — unnest(@x::text[]) on
// the SQL side (queries/alert_routes.sql's own comment on
// ReplaceRouteSeverities).
func statesToStrings(in []rules.State) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func eventsToStrings(in []alerts.EventKind) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

// toRoute assembles the package's own Route from one gen.AlertRoute row and
// its already-grouped selections.
func toRoute(r gen.AlertRoute, sel selection) Route {
	return Route{
		ID:         r.ID,
		OrgID:      r.OrgID,
		Name:       r.Name,
		Enabled:    r.Enabled,
		Scope:      Scope(r.Scope),
		ScopeID:    scopeIDFromColumns(r.Scope, r.ScopeGroupID, r.ScopeHostID),
		Severities: sel.Severities,
		Events:     sel.Events,
		Channels:   sel.Channels,
		Revision:   r.Revision,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
}

// replaceSelections replaces all three of routeID's selection tables inside
// the caller's own transaction q runs on. A foreign-key violation can only
// be ReplaceRouteChannels' own composite key onto notification_channels
// (severities and events reference nothing outside this table's own CHECK
// constraints), so it is reported as ErrInvalidInput naming the channel —
// the same mapping notify.ReplaceScheduleRules gives its own "unknown
// channel" case.
func (s *Store) replaceSelections(ctx context.Context, q *gen.Queries, orgID, routeID uuid.UUID, in Input) error {
	if err := q.ReplaceRouteSeverities(ctx, gen.ReplaceRouteSeveritiesParams{
		OrgID: orgID, RouteID: routeID, Severities: statesToStrings(in.Severities),
	}); err != nil {
		return fmt.Errorf("replace route severities: %w", err)
	}
	if err := q.ReplaceRouteEvents(ctx, gen.ReplaceRouteEventsParams{
		OrgID: orgID, RouteID: routeID, Kinds: eventsToStrings(in.Events),
	}); err != nil {
		return fmt.Errorf("replace route events: %w", err)
	}
	if err := q.ReplaceRouteChannels(ctx, gen.ReplaceRouteChannelsParams{
		OrgID: orgID, RouteID: routeID, ChannelIds: in.Channels,
	}); err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: a channel does not belong to this organization", ErrInvalidInput)
		}
		return fmt.Errorf("replace route channels: %w", err)
	}
	return nil
}

// Create inserts id's row and all three selections in one transaction: a
// route that exists with no channels because the second statement failed
// is exactly the silently-ineffective state this feature exists to
// prevent, so the whole thing commits together or not at all.
func (s *Store) Create(ctx context.Context, orgID, id uuid.UUID, in Input, now time.Time) (Route, error) {
	groupID, hostID := scopeColumns(in.Scope, in.ScopeID)

	tx, err := s.d.Begin(ctx)
	if err != nil {
		return Route{}, fmt.Errorf("begin create route: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	row, err := q.CreateAlertRoute(ctx, gen.CreateAlertRouteParams{
		ID: id, OrgID: orgID, Name: in.Name, Enabled: in.Enabled, Scope: string(in.Scope),
		ScopeGroupID: groupID, ScopeHostID: hostID, CreatedAt: now, UpdatedAt: now,
	})
	if db.IsUniqueViolation(err) {
		return Route{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
	}
	if err != nil {
		return Route{}, fmt.Errorf("create route: %w", err)
	}

	if err := s.replaceSelections(ctx, q, orgID, id, in); err != nil {
		return Route{}, err
	}

	sel, err := s.selectionsByRoute(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, fmt.Errorf("commit create route: %w", err)
	}
	return toRoute(row, sel[id]), nil
}

// Update replaces id's row and all three selections in one transaction, the
// same all-or-nothing posture Create takes. revision is the If-Match style
// optimistic lock (backlog 1.31): UpdateAlertRoute is :execrows, not :one
// with RETURNING, the same choice maintenance.SQLStore.Update already makes
// for the same reason — zero rows affected is the one signal this needs,
// and re-reading the row (still inside this transaction) is what tells a
// stale revision (ErrRevisionMismatch) from an id that no longer exists at
// all (ErrNotFound) rather than trusting the statement to distinguish them
// itself.
func (s *Store) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input, now time.Time) (Route, error) {
	groupID, hostID := scopeColumns(in.Scope, in.ScopeID)

	tx, err := s.d.Begin(ctx)
	if err != nil {
		return Route{}, fmt.Errorf("begin update route: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	n, err := q.UpdateAlertRoute(ctx, gen.UpdateAlertRouteParams{
		Name: in.Name, Enabled: in.Enabled, Scope: string(in.Scope),
		ScopeGroupID: groupID, ScopeHostID: hostID, UpdatedAt: now,
		OrgID: orgID, ID: id, Revision: revision,
	})
	if db.IsUniqueViolation(err) {
		return Route{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
	}
	if err != nil {
		return Route{}, fmt.Errorf("update route: %w", err)
	}
	if n == 0 {
		if _, gerr := q.GetAlertRoute(ctx, gen.GetAlertRouteParams{OrgID: orgID, ID: id}); gerr != nil {
			if errors.Is(gerr, pgx.ErrNoRows) {
				return Route{}, ErrNotFound
			}
			return Route{}, fmt.Errorf("re-read route after conflict: %w", gerr)
		}
		return Route{}, ErrRevisionMismatch
	}

	if err := s.replaceSelections(ctx, q, orgID, id, in); err != nil {
		return Route{}, err
	}

	row, err := q.GetAlertRoute(ctx, gen.GetAlertRouteParams{OrgID: orgID, ID: id})
	if err != nil {
		return Route{}, fmt.Errorf("re-read updated route: %w", err)
	}
	sel, err := s.selectionsByRoute(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, fmt.Errorf("commit update route: %w", err)
	}
	return toRoute(row, sel[id]), nil
}

// Get implements the Store's single-route read. ErrNotFound covers both a
// route that never existed and one that belongs to another organization —
// the org_id predicate in GetAlertRoute makes the two indistinguishable by
// design, the same posture notify.SQLStore.GetChannel and
// maintenance.SQLStore.Get already take.
func (s *Store) Get(ctx context.Context, orgID, id uuid.UUID) (Route, error) {
	q := gen.New(s.d)
	row, err := q.GetAlertRoute(ctx, gen.GetAlertRouteParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if err != nil {
		return Route{}, fmt.Errorf("get route: %w", err)
	}
	sel, err := s.selectionsByRoute(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}
	return toRoute(row, sel[id]), nil
}

// List returns every one of org's routes, by name (ListAlertRoutes' own
// ORDER BY), each with its selections attached from one shared
// selectionsByRoute call rather than one selection query per route.
func (s *Store) List(ctx context.Context, orgID uuid.UUID) ([]Route, error) {
	q := gen.New(s.d)
	rows, err := q.ListAlertRoutes(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	sel, err := s.selectionsByRoute(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Route, len(rows))
	for i, r := range rows {
		out[i] = toRoute(r, sel[r.ID])
	}
	return out, nil
}

// Delete removes id and its selections, which cascade off alert_routes' own
// id (migration 00052's composite FKs) — DeleteAlertRoute's own doc
// comment says so. ErrNotFound when org_id/id matches nothing.
func (s *Store) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := gen.New(s.d).DeleteAlertRoute(ctx, gen.DeleteAlertRouteParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete route: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListEnabled returns org's enabled routes, for a caller running inside its
// own transaction — the follower's per-org tick (a later task), the same
// shape internal/alerts' own OpenForHost and Execute take a caller-owned
// pgx.Tx for. There is no "is this route enabled" predicate in
// ListAlertRoutes itself: the query already reads the organization's full
// route list in one round trip the way List's own does, and filtering that
// slice in Go costs nothing more than a second query would have, so a
// dedicated SQL statement was not worth adding for this.
func (s *Store) ListEnabled(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) ([]Route, error) {
	q := gen.New(tx)
	rows, err := q.ListAlertRoutes(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list enabled routes: %w", err)
	}
	sel, err := s.selectionsByRoute(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		out = append(out, toRoute(r, sel[r.ID]))
	}
	return out, nil
}
