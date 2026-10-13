// SQLStore is the only part of this package that touches a database: route
// CRUD against migration 00052's alert_routes table. Its four selection
// tables (alert_route_severities, alert_route_events, alert_route_channels,
// alert_route_breakthrough_severities, migration 00054) have their own read
// and write side in selections_sql.go. Modelled on
// internal/alerts/store_sql.go throughout — how it reaches *db.DB, how it
// maps "no rows" to ErrNotFound and "zero rows updated after a successful
// read" to ErrRevisionMismatch.
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

// toRoute assembles the package's own Route from one gen.AlertRoute row,
// its already-grouped selections and its breakthrough severities.
func toRoute(r gen.AlertRoute, sel selection, breakthrough []rules.State) Route {
	return Route{
		ID:           r.ID,
		OrgID:        r.OrgID,
		Name:         r.Name,
		Enabled:      r.Enabled,
		Scope:        Scope(r.Scope),
		ScopeID:      scopeIDFromColumns(r.Scope, r.ScopeGroupID, r.ScopeHostID),
		Severities:   sel.Severities,
		Events:       sel.Events,
		Channels:     sel.Channels,
		Window:       windowFromColumns(r.WindowCron, r.WindowDurationSeconds, r.WindowTimezone),
		Breakthrough: breakthrough,
		RotationID:   db.UUIDPtr(r.RotationID),
		Revision:     r.Revision,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

// routeRowError maps the row-level constraint failures CreateAlertRoute and
// UpdateAlertRoute can raise. The unique one is the route's own name; the
// three foreign keys are the things a route POINTS AT — its rotation, and
// the host or monitoring group its scope names — any of which may have been
// deleted since the editor loaded them (delete a rotation in one tab, save
// a route still naming it in another). Unmapped, those surfaced as a bare
// wrapped database error and the API answered 500 for an ordinary race;
// replaceSelections (selections_sql.go) and oncall.CreateException already
// map the same class on their own tables. ConstraintName is what tells the
// three apart, since alert_routes carries more than one.
func routeRowError(err error, name string) error {
	switch {
	case db.IsUniqueViolation(err):
		return fmt.Errorf("%w: %s", ErrNameTaken, name)
	case db.IsForeignKeyViolation(err):
		switch db.ConstraintName(err) {
		case "alert_routes_org_id_scope_group_id_fkey":
			return fmt.Errorf("%w: no such monitoring group in this organization", ErrInvalidInput)
		case "alert_routes_org_id_scope_host_id_fkey":
			return fmt.Errorf("%w: no such host in this organization", ErrInvalidInput)
		case "alert_routes_rotation_fkey":
			return fmt.Errorf("%w: no such on-call rotation in this organization", ErrInvalidInput)
		default:
			return fmt.Errorf("%w: this route references something that does not exist in this organization", ErrInvalidInput)
		}
	default:
		return nil
	}
}

// Create inserts id's row and all four selections in one transaction: a
// route that exists with no channels because the second statement failed
// is exactly the silently-ineffective state this feature exists to
// prevent, so the whole thing commits together or not at all.
func (s *Store) Create(ctx context.Context, orgID, id uuid.UUID, in Input, now time.Time) (Route, error) {
	groupID, hostID := scopeColumns(in.Scope, in.ScopeID)
	cron, durationSeconds, timezone := windowColumns(in.Window)

	tx, err := s.d.Begin(ctx)
	if err != nil {
		return Route{}, fmt.Errorf("begin create route: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	row, err := q.CreateAlertRoute(ctx, gen.CreateAlertRouteParams{
		ID: id, OrgID: orgID, Name: in.Name, Enabled: in.Enabled, Scope: string(in.Scope),
		ScopeGroupID: groupID, ScopeHostID: hostID,
		WindowCron: cron, WindowDurationSeconds: durationSeconds, WindowTimezone: timezone,
		RotationID: db.NullUUID(in.RotationID), CreatedAt: now, UpdatedAt: now,
	})
	if mapped := routeRowError(err, in.Name); mapped != nil {
		return Route{}, mapped
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
	breakthrough, err := s.breakthroughByRoute(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, fmt.Errorf("commit create route: %w", err)
	}
	return toRoute(row, sel[id], breakthrough[id]), nil
}

// Update replaces id's row and all four selections in one transaction, the
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
	cron, durationSeconds, timezone := windowColumns(in.Window)

	tx, err := s.d.Begin(ctx)
	if err != nil {
		return Route{}, fmt.Errorf("begin update route: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	n, err := q.UpdateAlertRoute(ctx, gen.UpdateAlertRouteParams{
		Name: in.Name, Enabled: in.Enabled, Scope: string(in.Scope),
		ScopeGroupID: groupID, ScopeHostID: hostID,
		WindowCron: cron, WindowDurationSeconds: durationSeconds, WindowTimezone: timezone,
		RotationID: db.NullUUID(in.RotationID), UpdatedAt: now,
		OrgID: orgID, ID: id, Revision: revision,
	})
	if mapped := routeRowError(err, in.Name); mapped != nil {
		return Route{}, mapped
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
	breakthrough, err := s.breakthroughByRoute(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, fmt.Errorf("commit update route: %w", err)
	}
	return toRoute(row, sel[id], breakthrough[id]), nil
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
	breakthrough, err := s.breakthroughByRoute(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}
	return toRoute(row, sel[id], breakthrough[id]), nil
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
	breakthrough, err := s.breakthroughByRoute(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Route, len(rows))
	for i, r := range rows {
		out[i] = toRoute(r, sel[r.ID], breakthrough[r.ID])
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
	breakthrough, err := s.breakthroughByRoute(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		out = append(out, toRoute(r, sel[r.ID], breakthrough[r.ID]))
	}
	return out, nil
}
