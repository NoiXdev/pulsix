// Store's read and write side for the route's four selection tables
// (alert_route_severities, alert_route_events, alert_route_channels,
// alert_route_breakthrough_severities), split out of store_sql.go's own
// alert_routes CRUD so that file stays about the route row itself.
package alertroutes

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/rules"
)

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

// statesToStrings and eventsToStrings convert Input's typed selections to
// the text[] the Insert* queries take — unnest(@x::text[]) on the SQL side
// (queries/alert_routes.sql's own comment on InsertRouteSeverities).
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

// replaceSelections replaces all four of routeID's selection tables inside
// the caller's own transaction q runs on. Each is a DELETE followed by an
// INSERT sent as two separate statements, not the single WITH-based
// statement this package originally shipped: Postgres documents a
// data-modifying CTE's sub-statements as running against one shared
// snapshot with their relative order unspecified when, as here, the INSERT
// never reads the DELETE's own output
// (https://www.postgresql.org/docs/current/queries-with.html#QUERIES-WITH-MODIFYING,
// quoted in full on DeleteRouteSeverities, queries/alert_routes.sql). That
// combined shape is what TestStoreUpdateReplacesBreakthroughOverAnExistingRow
// caught losing a severity that should have survived an update unchanged —
// the INSERT's ON CONFLICT DO NOTHING left the old row in place while the
// DELETE, running second instead of first, removed it anyway — the same
// failure mode DeleteRotationChannels' own comment (queries/oncall.sql)
// already paid for once against oncall_rotation_channels' position-keyed
// primary key. Two statements in sequence on this transaction remove the
// ambiguity: each DELETE commits before its INSERT ever runs.
//
// A foreign-key violation can only be InsertRouteChannels' own composite
// key onto notification_channels (severities, events and breakthrough
// severities reference nothing outside this table's own CHECK constraints),
// so it is reported as ErrInvalidInput naming the channel — the same
// mapping notify.ReplaceScheduleRules gives its own "unknown channel" case.
func (s *Store) replaceSelections(ctx context.Context, q *gen.Queries, orgID, routeID uuid.UUID, in Input) error {
	if err := q.DeleteRouteSeverities(ctx, gen.DeleteRouteSeveritiesParams{OrgID: orgID, RouteID: routeID}); err != nil {
		return fmt.Errorf("delete route severities: %w", err)
	}
	if err := q.InsertRouteSeverities(ctx, gen.InsertRouteSeveritiesParams{
		OrgID: orgID, RouteID: routeID, Severities: statesToStrings(in.Severities),
	}); err != nil {
		return fmt.Errorf("insert route severities: %w", err)
	}
	if err := q.DeleteRouteEvents(ctx, gen.DeleteRouteEventsParams{OrgID: orgID, RouteID: routeID}); err != nil {
		return fmt.Errorf("delete route events: %w", err)
	}
	if err := q.InsertRouteEvents(ctx, gen.InsertRouteEventsParams{
		OrgID: orgID, RouteID: routeID, Kinds: eventsToStrings(in.Events),
	}); err != nil {
		return fmt.Errorf("insert route events: %w", err)
	}
	if err := q.DeleteRouteChannels(ctx, gen.DeleteRouteChannelsParams{OrgID: orgID, RouteID: routeID}); err != nil {
		return fmt.Errorf("delete route channels: %w", err)
	}
	if err := q.InsertRouteChannels(ctx, gen.InsertRouteChannelsParams{
		OrgID: orgID, RouteID: routeID, ChannelIds: in.Channels,
	}); err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: a channel does not belong to this organization", ErrInvalidInput)
		}
		return fmt.Errorf("insert route channels: %w", err)
	}
	if err := q.DeleteRouteBreakthroughSeverities(ctx, gen.DeleteRouteBreakthroughSeveritiesParams{OrgID: orgID, RouteID: routeID}); err != nil {
		return fmt.Errorf("delete route breakthrough severities: %w", err)
	}
	if err := q.InsertRouteBreakthroughSeverities(ctx, gen.InsertRouteBreakthroughSeveritiesParams{
		OrgID: orgID, RouteID: routeID, Severities: statesToStrings(in.Breakthrough),
	}); err != nil {
		return fmt.Errorf("insert route breakthrough severities: %w", err)
	}
	return nil
}
