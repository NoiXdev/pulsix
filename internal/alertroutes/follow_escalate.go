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
//
// routes is Tick's own already-read ListEnabled slice, passed in rather
// than read a second time: this pass needs it purely for the two things
// ChainsNeedingAttentionBase's own structural query deliberately does NOT
// check (Chain's own doc comment, and the query's) — a route's severity
// selection and its scope — and the forward pass has already paid for
// that exact read this same tick.
func (f *Follower) escalatePass(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, routes []Route, now time.Time) error {
	chains, err := f.store.ChainsNeedingAttention(ctx, tx, orgID, f.batch)
	if err != nil {
		return fmt.Errorf("chains needing attention: %w", err)
	}
	if len(chains) == 0 {
		return nil
	}

	routesByID := make(map[uuid.UUID]Route, len(routes))
	for _, rt := range routes {
		routesByID[rt.ID] = rt
	}

	// One resolution for the whole pass: its host-groups cache
	// (hostGroupsFor, follow_resolve.go) is what dueStep's own scope
	// re-check shares across every chain on the same host, and its
	// rotation cache (oncall) is exactly as valid for a step's channel
	// source as it is for a route's own, since both depend only on a
	// rotation id and on `now`. Built before the read loop below, not
	// after it like the forward pass's own per-tick resolution is built
	// before ITS read loop — the same shape, for the same reason.
	res := &resolution{f: f, tx: tx, orgID: orgID, now: now,
		groups: map[uuid.UUID][]uuid.UUID{}, oncall: map[uuid.UUID]uuid.UUID{}}

	var dueSteps []due
	for _, c := range chains {
		rt, ok := routesByID[c.RouteID]
		if !ok {
			// Not reachable in the same transaction ChainsNeedingAttention
			// just read routes in — its own JOIN requires r.enabled, so
			// every route it can name is also in routes. Guarded rather
			// than trusted: a chain this pass cannot place a route for is
			// one it cannot safely evaluate, not a panic.
			continue
		}
		ready, step, alert, err := f.dueStep(ctx, res, orgID, c, rt, now)
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

	for _, d := range dueSteps {
		if err := f.fireStep(ctx, tx, res, orgID, d, now); err != nil {
			return err
		}
	}
	return nil
}

// dueStep applies every gate decision 4 names that the store's own
// structural query cannot (ChainsNeedingAttentionBase's doc comment): the
// route's severity selection, its SCOPE (review, Important 5 — both this
// query's own doc comment and Chain's claim the follower re-runs match.go's
// scope/severity logic, which was true of severity alone until this fix),
// acknowledgement, and every one of alerts.Mute's three mute reasons. Only
// once a chain clears all of them is DueStep (escalate.go) itself asked.
func (f *Follower) dueStep(ctx context.Context, res *resolution, orgID uuid.UUID, c Chain, rt Route, now time.Time) (bool, Step, alerts.AlertWithHost, error) {
	if !slices.Contains(rt.Severities, c.Severity) {
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

	hostGroups, err := res.hostGroupsFor(ctx, alert.Subject.HostID)
	if err != nil {
		return false, Step{}, alerts.AlertWithHost{}, err
	}
	if !scopeCovers(rt, alert.Subject.HostID, hostGroups) {
		// Paused, the same as a severity leaving the route's set: a route
		// narrowed to a different host or group — or a host moved out of
		// the group a route was scoped to — must stop this chain's clock
		// exactly as losing severity coverage does, not keep escalating
		// for a host the route no longer names at all. match.go's own
		// scopeCovers is reused rather than re-decided, the same posture
		// this file already takes for channelsOf.
		return false, Step{}, alerts.AlertWithHost{}, nil
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
	// The sixth anchor (decision 5), which ChainsNeedingAttentionBase's own
	// doc comment leaves to the follower on purpose: whether a maintenance
	// occurrence covered this alert is a fact about cron expressions
	// evaluated in Go (internal/maintenance), not a column SQL can read.
	// If one already covered the alert somewhere between the chain's own
	// anchor and the moment its next step would naively come due, and has
	// since ended, that step's delay counts from the occurrence's end, not
	// from the stale anchor — the same "full delay from the moment it
	// became newly urgent again" rule the other five anchors already give.
	//
	// This is the resume path for TWO of alerts.Mute's three reasons, not
	// one: alerts.Mute's own windows argument carries both a maintenance
	// window AND an operator silence as maintenance.Occurrence values,
	// distinguished only by Reason — WindowsForHost returns both, and this
	// loop folds in whichever one it finds with no need to filter by
	// Reason. The snooze reason needs no fold here at all: its own end is
	// already the plain snoozed_until column ChainsNeedingAttentionBase
	// reads directly (anchor 2, the query's own doc comment).
	//
	// Sampled at two instants, not at `now`: WindowsForHost only answers
	// "what covers this instant", so by the time an occurrence is no
	// longer active AT now there is nothing left in its answer to read an
	// End from.
	//
	//   - the anchor itself: catches an occurrence that already covered
	//     the alert there and has since ended — a mute that started at or
	//     before the previous step's own firing.
	//   - the anchor plus the next step's OWN delay — the instant that
	//     step would have come due with no mute in the picture at all:
	//     catches an occurrence that started after the anchor but was
	//     already covering the alert by the time it would otherwise have
	//     fired, which the mute check immediately above has been holding
	//     on every tick since, exactly the ordinary "it came due while
	//     muted" shape decision 4 describes.
	//
	// What this still does NOT find is an occurrence that starts AFTER
	// both of those instants and has already ended by `now` — one that
	// began and ended entirely inside the gap between this chain's anchor
	// and whatever it would naively be due at, without ever covering
	// either sampled point. WindowsForHost's own shape (active-at-an-
	// instant, not active-over-a-range) has no way to see one, and fixing
	// that fully needs an interval query this pass's own files do not
	// have. Accepted as shippable: ticks run every DefaultFollowInterval,
	// occurrences run minutes to hours, and the one failure direction left
	// is "fires a little early", never "never fires at all".
	anchor := cs.LastFiredAt
	if cs.LastUrgentAt.After(anchor) {
		anchor = cs.LastUrgentAt
	}
	samples := []time.Time{anchor}
	if d, ok := stepDelay(cs.Steps, cs.FiredUpTo+1); ok {
		samples = append(samples, anchor.Add(d))
	}
	for _, at := range samples {
		if at.IsZero() || !at.Before(now) {
			continue
		}
		atSample, err := f.mutes.WindowsForHost(ctx, orgID, alert.Subject.HostID, at)
		if err != nil {
			return false, Step{}, alerts.AlertWithHost{}, fmt.Errorf(
				"windows for host %s at %s: %w", alert.Subject.HostID, at, err)
		}
		for _, w := range atSample {
			if w.End.After(cs.LastUrgentAt) && !w.End.After(now) {
				cs.LastUrgentAt = w.End
			}
		}
	}

	step, ok := DueStep(cs, now)
	return ok, step, alert, nil
}

// stepDelay finds position's own Delay in steps — the next unfired rung's
// delay, used only to pick the fold's second sample instant above (the
// moment that rung would naively come due with no mute in the picture at
// all). false when no step at that position exists, which DueStep itself
// will also find and refuse to answer for.
func stepDelay(steps []Step, position int) (time.Duration, bool) {
	for _, s := range steps {
		if s.Position == position {
			return s.Delay, true
		}
	}
	return 0, false
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
