// The escalation ladder's own store: ReplaceSteps writes a route's steps,
// ClaimStep writes the anti-join ledger (migration 00056's
// alert_escalations), and ChainsNeedingAttention is the read the
// follower's second pass (a later task) drives its sweep from. Split out
// of store_sql.go the same way ledger_sql.go already split v0.4b's own
// delivery ledger out — one responsibility per file.
//
// Every method here takes a caller-owned pgx.Tx, never s.d directly, the
// same posture ledger_sql.go's own doc comment explains: the follower's
// tick (a later task) runs the whole chain pass — reading due chains,
// claiming a step, enqueuing its delivery — inside one transaction per
// organization, so a decision and its ledger row commit or roll back
// together. That transaction carries no pulsix.org_id row-level-security
// binding, so every statement below names org_id in its own WHERE/params,
// the same reason ledger_sql.go's own queries do.
package alertroutes

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/rules"
)

// Chain pairs a ChainState (escalate.go) with the identity it belongs to:
// the (alert, route) pair ChainsNeedingAttention found, and the alert's
// own severity at read time — the follower (a later task) needs it to
// re-run match.go's own severity check before ever calling DueStep, since
// this query deliberately does not (ChainsNeedingAttentionBase's own doc
// comment, alert_escalations.sql, says why).
type Chain struct {
	AlertID  uuid.UUID
	RouteID  uuid.UUID
	Severity rules.State
	State    ChainState
}

// ReplaceSteps writes routeID's ladder: every row in alert_route_steps and
// alert_route_step_channels for it, replaced wholesale. Delete-then-insert
// as two statements sent in sequence on tx — never one WITH-based
// statement — the same posture replaceSelections (selections_sql.go,
// store_sql.go) already takes for the route's own four selection tables;
// DeleteRouteSteps' own doc comment (alert_escalations.sql) explains why a
// data-modifying CTE was rejected for this exact shape once already.
//
// steps may be empty — a route with no escalation ladder at all, the
// default for every route created before this feature existed — and that
// is a legal call: DeleteRouteSteps still runs (clearing a ladder an editor
// removed entirely), and the insert loop below simply does nothing.
func (s *Store) ReplaceSteps(ctx context.Context, tx pgx.Tx, orgID, routeID uuid.UUID, steps []Step) error {
	q := gen.New(tx)
	if err := q.DeleteRouteSteps(ctx, gen.DeleteRouteStepsParams{OrgID: orgID, RouteID: routeID}); err != nil {
		return fmt.Errorf("delete route steps: %w", err)
	}
	for _, st := range steps {
		if err := q.InsertRouteStep(ctx, gen.InsertRouteStepParams{
			OrgID: orgID, RouteID: routeID,
			Position:     int32(st.Position),        //nolint:gosec // the service validates a step's position before it ever reaches the store
			DelaySeconds: int32(st.Delay.Seconds()), //nolint:gosec // same validation covers the delay
			RotationID:   db.NullUUID(st.RotationID),
		}); err != nil {
			return fmt.Errorf("insert route step %d: %w", st.Position, err)
		}
		// Always called, even with a nil/empty ChannelIDs — a step naming
		// a rotation instead has nothing to insert, and
		// InsertRouteStepChannels' own unnest(NULL-or-empty) is a legal
		// no-op, the same "empty array clears the selection to nothing"
		// reading InsertRouteChannels already relies on.
		if err := q.InsertRouteStepChannels(ctx, gen.InsertRouteStepChannelsParams{
			OrgID: orgID, RouteID: routeID,
			Position:   int32(st.Position), //nolint:gosec // see above
			ChannelIds: st.ChannelIDs,
		}); err != nil {
			return fmt.Errorf("insert route step %d channels: %w", st.Position, err)
		}
	}
	return nil
}

// ClaimStep writes the ledger row that makes position fired for (alertID,
// routeID). It reports false, with no error, when the row already
// existed — ClaimEscalationStep's own ON CONFLICT DO NOTHING is the whole
// mechanism — which is how two managers ticking at once, or one tick
// overlapping the next, both attempting the identical claim, end up a
// quiet no-op rather than an error that would abort the whole pass.
func (s *Store) ClaimStep(ctx context.Context, tx pgx.Tx, orgID, alertID, routeID uuid.UUID, position int, at time.Time) (bool, error) {
	n, err := gen.New(tx).ClaimEscalationStep(ctx, gen.ClaimEscalationStepParams{
		OrgID: orgID, AlertID: alertID, RouteID: routeID,
		Position: int32(position), //nolint:gosec // a ladder's own step number, bounded by ReplaceSteps' own insert
		FiredAt:  at,
	})
	if err != nil {
		return false, fmt.Errorf("claim escalation step: %w", err)
	}
	return n > 0, nil
}

// ChainsNeedingAttention returns one Chain per (open alert, enabled route)
// pair whose ladder has at least one step nobody has climbed to yet.
// ChainsNeedingAttentionBase (alert_escalations.sql) runs the structural
// filter in one round trip; ListEscalationStepsForRoutes fills in each
// distinct route's own ladder in a second — the same N+1-avoiding split
// ListRouteSelections already takes for a route's own selection tables,
// reused here because reading one route's steps per row would be the
// identical mistake.
func (s *Store) ChainsNeedingAttention(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, limit int) ([]Chain, error) {
	q := gen.New(tx)
	rows, err := q.ChainsNeedingAttentionBase(ctx, gen.ChainsNeedingAttentionBaseParams{
		OrgID: orgID, Lim: int32(limit), //nolint:gosec // the follower's own per-tick batch size, never user input
	})
	if err != nil {
		return nil, fmt.Errorf("chains needing attention: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	routeIDs := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		if !seen[r.RouteID] {
			seen[r.RouteID] = true
			routeIDs = append(routeIDs, r.RouteID)
		}
	}
	stepRows, err := q.ListEscalationStepsForRoutes(ctx, gen.ListEscalationStepsForRoutesParams{
		OrgID: orgID, RouteIds: routeIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list escalation steps for routes: %w", err)
	}
	steps := stepsByRoute(stepRows)

	out := make([]Chain, len(rows))
	for i, r := range rows {
		var lastFired time.Time
		if r.LastFiredAt.Valid {
			lastFired = r.LastFiredAt.Time
		}
		// LastUrgentAt is the later of the alert's own most recent
		// severity_raised/ack_voided event and its snoozed_until column —
		// two of spec decision 5's three anchors, the third (the end of a
		// maintenance occurrence) left to the follower
		// (ChainsNeedingAttentionBase's own doc comment explains why).
		// has_urgent_event, not a zero-value check, is what tells "no
		// event at all" apart from "an event at the epoch sentinel" —
		// row.LastEventAt is the COALESCE-to-'epoch' value the query uses
		// solely to keep the column NOT NULL for sqlc, never a real
		// anchor on its own.
		var lastUrgent time.Time
		if r.HasUrgentEvent {
			lastUrgent = r.LastEventAt
		}
		if r.SnoozedUntil.Valid && r.SnoozedUntil.Time.After(lastUrgent) {
			lastUrgent = r.SnoozedUntil.Time
		}
		out[i] = Chain{
			AlertID:  r.AlertID,
			RouteID:  r.RouteID,
			Severity: rules.State(r.Severity),
			State: ChainState{
				Steps:        steps[r.RouteID],
				FiredUpTo:    int(r.FiredUpTo),
				LastFiredAt:  lastFired,
				LastUrgentAt: lastUrgent,
			},
		}
	}
	return out, nil
}

// stepsByRoute groups ListEscalationStepsForRoutes' own flat, one-row-per-
// (step, channel) rows — that query's own LEFT JOIN, the reason a step
// with no fixed channels (a rotation step, or an empty one) still
// surfaces exactly once with channel_id NULL — back into each route's
// ordered []Step. Rows arrive ordered by (route_id, position), so a run
// of rows for one step is always contiguous and never needs re-visiting
// once a later position or route starts.
func stepsByRoute(rows []gen.ListEscalationStepsForRoutesRow) map[uuid.UUID][]Step {
	out := map[uuid.UUID][]Step{}
	var cur *Step
	var curRoute uuid.UUID
	curPos := -1
	for _, r := range rows {
		pos := int(r.Position)
		if cur == nil || r.RouteID != curRoute || pos != curPos {
			out[r.RouteID] = append(out[r.RouteID], Step{
				Position:   pos,
				Delay:      time.Duration(r.DelaySeconds) * time.Second,
				RotationID: db.UUIDPtr(r.RotationID),
			})
			cur = &out[r.RouteID][len(out[r.RouteID])-1]
			curRoute, curPos = r.RouteID, pos
		}
		if id := db.UUIDPtr(r.ChannelID); id != nil {
			cur.ChannelIDs = append(cur.ChannelIDs, *id)
		}
	}
	return out
}
