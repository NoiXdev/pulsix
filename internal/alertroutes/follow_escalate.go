// The follower's second pass (v0.4d, spec docs/superpowers/specs/
// 2026-10-16-v0.4d-escalation-chains-design.md, "The sweep"): every open,
// unacknowledged, unmuted alert with a matching enabled route whose next
// unfired step has come due. The base row every chain measures from — step
// 0, the route's own first real delivery — is written by the FIRST pass
// (claimBases, follow.go), at the moment that delivery is certain; nothing
// here ever claims position 0, and DueStep (escalate.go) already refuses
// to answer for a chain that has none (cs.FiredUpTo < 0).
//
// Called from Tick (follow.go) after the event pass, inside the same
// transaction and advisory lock, so a step's claim and its delivery commit
// or roll back together exactly as the forward pass's ledger write and
// enqueue already do (follow.go's own file comment is the reason, restated
// here for a second axis of state rather than re-argued).
package alertroutes

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/rules"
)

// due is one chain whose next step has actually come due this tick, with
// the alert already read once so firing it never re-fetches what deciding
// it already needed.
type due struct {
	chain Chain
	step  Step
	alert alerts.AlertWithHost
}

// escalatePass reads every chain that might have a rung to climb, decides
// which of them actually do, and only then claims and delivers — the same
// read-everything-before-writing-anything discipline Tick's own event pass
// follows (follow.go's file comment), and for the identical reason: a step
// claimed earlier in this loop must not change what a later chain's own
// DueStep call sees for the identical `now`, and none of them do, because
// nothing is claimed until this loop is over.
func (f *Follower) escalatePass(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, now time.Time) error {
	chains, err := f.store.ChainsNeedingAttention(ctx, tx, orgID, f.batch)
	if err != nil {
		return fmt.Errorf("chains needing attention: %w", err)
	}
	if len(chains) == 0 {
		return nil
	}

	// ChainsNeedingAttentionBase's own join already restricts to enabled
	// routes, so every route a chain names below is in this map — this
	// read exists purely for the one thing that query deliberately does
	// NOT check (Chain's own doc comment, and the query's): whether the
	// alert's current severity is still in the route's own selection.
	routes, err := f.store.ListEnabled(ctx, tx, orgID)
	if err != nil {
		return fmt.Errorf("list enabled routes: %w", err)
	}
	severities := make(map[uuid.UUID][]rules.State, len(routes))
	for _, rt := range routes {
		severities[rt.ID] = rt.Severities
	}

	var dueSteps []due
	for _, c := range chains {
		ready, step, alert, err := f.dueStep(ctx, orgID, c, severities[c.RouteID], now)
		if err != nil {
			return err
		}
		if ready {
			dueSteps = append(dueSteps, due{chain: c, step: step, alert: alert})
		}
	}
	if len(dueSteps) == 0 {
		return nil
	}

	// One resolution for the whole pass, built fresh rather than reused
	// from the event pass's own (Tick never keeps that one alive this
	// long): channelsOf's rotation cache (oncall) is exactly as valid
	// here as there, since it depends only on a rotation id and on `now`,
	// neither of which differs between a route's own channels and a
	// step's. Nil groups/windows/alerts/gates maps are never touched —
	// channelsOf reads none of them.
	res := &resolution{f: f, tx: tx, orgID: orgID, now: now, oncall: map[uuid.UUID]uuid.UUID{}}
	for _, d := range dueSteps {
		if err := f.fireStep(ctx, tx, res, orgID, d, now); err != nil {
			return err
		}
	}
	return nil
}

// dueStep applies every gate decision 4 names that the store's own
// structural query cannot (ChainsNeedingAttentionBase's doc comment): the
// route's severity selection, acknowledgement, and every one of
// alerts.Mute's three mute reasons. Only once a chain clears all of them
// is DueStep (escalate.go) itself asked.
func (f *Follower) dueStep(ctx context.Context, orgID uuid.UUID, c Chain, routeSeverities []rules.State, now time.Time) (bool, Step, alerts.AlertWithHost, error) {
	if !slices.Contains(routeSeverities, c.Severity) {
		// Paused, not stopped (decision 4): the route's own selection no
		// longer covers this alert's current severity. It resumes
		// matching, and the chain picks up at the next unfired step with
		// its own full delay (decision 5), the moment it does again.
		return false, Step{}, alerts.AlertWithHost{}, nil
	}

	alert, err := f.mutes.GetAlert(ctx, orgID, c.AlertID)
	if err != nil {
		return false, Step{}, alerts.AlertWithHost{}, fmt.Errorf("get alert %s: %w", c.AlertID, err)
	}
	if acknowledged(alert.Alert) {
		// Stop (decision 2): acknowledged_severity already reads live
		// here — RaiseAlertSeverity clears it on every ack_voided write
		// (backlog 4.36, settled by an earlier v0.4d change) — so a
		// severity that later falls back below the old acknowledged mark
		// no longer reads as acknowledged either, and the chain would
		// resume rather than stay stopped if it ever did.
		return false, Step{}, alerts.AlertWithHost{}, nil
	}

	windows, err := f.mutes.WindowsForHost(ctx, orgID, alert.Subject.HostID, now)
	if err != nil {
		return false, Step{}, alerts.AlertWithHost{}, fmt.Errorf("windows for host %s: %w", alert.Subject.HostID, err)
	}
	if alerts.Mute(alert.Alert, windows, now).Muted {
		// Paused. No step fires while an alert is muted for any of
		// alerts.Mute's three reasons — the forward pass already treats a
		// mute the same way (decide's own StateSuppressed branch) — and
		// the chain resumes on its own once it lifts.
		return false, Step{}, alerts.AlertWithHost{}, nil
	}

	cs := c.State
	// The fourth anchor (decision 5), which ChainsNeedingAttentionBase's
	// own doc comment leaves to the follower on purpose: whether a
	// maintenance occurrence covered this alert is a fact about cron
	// expressions evaluated in Go (internal/maintenance), not a column
	// SQL can read. If one already covered the alert at the chain's own
	// anchor and has since ended, the next step's delay counts from that
	// end, not from the stale anchor — the same "full delay from the
	// moment it became newly urgent again" rule the other three anchors
	// already give.
	//
	// Sampled at the anchor itself, not at `now`: WindowsForHost only
	// answers "what covers this instant", so by the time an occurrence is
	// no longer active AT now there is nothing left in its answer to read
	// an End from. Querying at the anchor still finds every occurrence
	// that was already covering the alert there and has since ended —
	// the shape a pause naturally takes, since the mute check immediately
	// above already holds the chain for every tick the occurrence is
	// active. What this does NOT find is a maintenance window that both
	// started and ended strictly between the anchor and now without ever
	// covering either instant — WindowsForHost's own shape (active-at-an-
	// instant, not active-over-a-range) has no way to see one, and fixing
	// that is a store change outside this pass's own files. Left as a
	// documented gap rather than silently assumed away.
	anchor := cs.LastFiredAt
	if cs.LastUrgentAt.After(anchor) {
		anchor = cs.LastUrgentAt
	}
	if !anchor.IsZero() && anchor.Before(now) {
		atAnchor, err := f.mutes.WindowsForHost(ctx, orgID, alert.Subject.HostID, anchor)
		if err != nil {
			return false, Step{}, alerts.AlertWithHost{}, fmt.Errorf(
				"windows for host %s at %s: %w", alert.Subject.HostID, anchor, err)
		}
		for _, w := range atAnchor {
			if w.End.After(cs.LastUrgentAt) && !w.End.After(now) {
				cs.LastUrgentAt = w.End
			}
		}
	}

	step, ok := DueStep(cs, now)
	return ok, step, alert, nil
}

// acknowledged is the same live comparison the alert list's own filter
// uses (queries/alerts.sql): severity <= acknowledged_severity, with an
// empty acknowledged_severity read as never acknowledged rather than as
// rank zero coinciding with a real one — alerts never carries severity
// "ok" at all (alert.go's own doc comment), so every real severity ranks
// above the zero value regardless, but the explicit check says what is
// actually meant rather than relying on that coincidence.
func acknowledged(a alerts.Alert) bool {
	return a.AckSeverity != "" && rules.StateRank(a.Severity) <= rules.StateRank(a.AckSeverity)
}

// fireStep claims d's step and, only if the claim actually took effect,
// resolves its channels, delivers through the existing outbox when it
// reaches anybody, and records the 'escalated' event either way.
//
// ClaimStep's own ON CONFLICT DO NOTHING (escalate_sql.go) is what makes
// the false case a quiet no-op rather than an error: two managers ticking
// at once, or one tick overlapping the next, both resolving the identical
// step as due and racing to claim it, with only one of them actually
// delivering.
func (f *Follower) fireStep(ctx context.Context, tx pgx.Tx, res *resolution, orgID uuid.UUID, d due, now time.Time) error {
	claimed, err := f.store.ClaimStep(ctx, tx, orgID, d.chain.AlertID, d.chain.RouteID, d.step.Position, now)
	if err != nil {
		return fmt.Errorf("claim escalation step %d for alert %s route %s: %w",
			d.step.Position, d.chain.AlertID, d.chain.RouteID, err)
	}
	if !claimed {
		return nil
	}

	// channelsOf (follow_resolve.go) is handed a Route built from the
	// step's own two channel sources, not a stored one — a step is never
	// itself a route, but it offers the identical either/or (fixed list or
	// rotation) channelsOf already knows how to resolve. The route's own
	// id travels along so a warning channelsOf logs about a broken
	// rotation still names something an operator can find.
	channels := res.channelsOf(ctx, Route{ID: d.chain.RouteID, RotationID: d.step.RotationID, Channels: d.step.ChannelIDs})
	if len(channels) > 0 {
		if err := f.deliverEscalation(ctx, tx, orgID, d, channels, now); err != nil {
			return err
		}
	}
	// Written whether or not anybody was reached: v0.4b's own ruling for a
	// route that reaches nobody — it still fires, and says so — applies to
	// a rung exactly the same way (Review Focus 2). The ladder is never
	// stuck behind a deleted channel or a broken rotation, and the alert's
	// own timeline still shows that this rung was tried.
	return f.store.RecordEscalation(ctx, tx, orgID, d.chain.AlertID, d.step.Position, channels, now)
}

// deliverEscalation sends one message for d's step. One message per rung,
// never coalesced with anything else this tick (decision 6: a step
// notifies its own channels once) — the batching the event pass's own
// enqueue exists for is for the common case of many events in one tick;
// an escalation is already the rare case that batching is not needed for.
func (f *Follower) deliverEscalation(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, d due, channels []uuid.UUID, now time.Time) error {
	slug, name, err := f.store.OrgContext(ctx, tx, orgID)
	if err != nil {
		return err
	}
	base := f.publicURL + "/o/" + slug
	item := notify.AlertItem{
		AlertID: d.chain.AlertID, HostID: d.alert.Subject.HostID, HostName: d.alert.HostName,
		CheckKey: d.alert.Subject.CheckKey, Item: d.alert.Subject.Item, Metric: d.alert.Subject.Metric,
		Severity: string(d.chain.Severity), Event: "escalated", At: now,
		URL: base + "/alerts?host=" + d.alert.Subject.HostID.String(),
	}
	ev := notify.Event{
		Kind: notify.EventAlertBatch, At: now,
		Org:    notify.OrgRef{ID: orgID, Slug: slug, Name: name},
		Alerts: &notify.AlertBatch{Count: 1, Items: []notify.AlertItem{item}},
		URL:    base + "/alerts",
	}
	// Content-derived, never the clock — batch.key's own doc comment
	// (follow_resolve.go) gives the full reasoning; the ids here (alert,
	// route, position) are exactly as stable an identity for one
	// escalation as a batch's sorted event-id hash is for a tick.
	key := fmt.Sprintf("alert.escalation:%s:%s:%d", d.chain.AlertID, d.chain.RouteID, d.step.Position)
	if err := f.enq.Enqueue(ctx, tx, orgID, key, ev, channels); err != nil {
		return fmt.Errorf("enqueue escalation for alert %s route %s step %d: %w",
			d.chain.AlertID, d.chain.RouteID, d.step.Position, err)
	}
	return nil
}
