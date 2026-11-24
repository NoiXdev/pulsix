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
//
// An alert already open the moment this release deploys never escalates,
// and that is correct rather than a gap: claimBases only runs on a
// delivery this same release just made, so a pair whose last delivery
// predates the upgrade has no base row and DueStep refuses it forever
// (cs.FiredUpTo < 0), the same as any other pair with no base. Nothing
// retroactively claims one for an alert that was already sitting open —
// this pass only ever reads what claimBases already wrote. Every alert
// opened from here on gets a base the first time any route delivers for
// it, so the gap is a one-time, self-healing artefact of the upgrade
// moment itself, not a standing hole: by the time an operator could
// notice it, every alert it could apply to has long since closed.
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

	cs, err := f.foldMaintenanceAnchor(ctx, orgID, alert.Subject.HostID, c.State, now)
	if err != nil {
		return false, Step{}, alerts.AlertWithHost{}, err
	}

	step, ok := DueStep(cs, now)
	return ok, step, alert, nil
}

// foldMaintenanceAnchor is the sixth anchor (decision 5), which
// ChainsNeedingAttentionBase's own doc comment leaves to the follower on
// purpose: whether a maintenance occurrence covered this alert is a fact
// about cron expressions evaluated in Go (internal/maintenance), not a
// column SQL can read. If one covered the alert at any instant since the
// chain's own anchor and has since ended, the next step's delay counts
// from the occurrence's end, not from the stale anchor — the same "full
// delay from the moment it became newly urgent again" rule the other
// anchors already give. It returns cs with LastUrgentAt moved to the
// latest such end, or cs unchanged.
//
// This is the resume path for TWO of alerts.Mute's three reasons, not
// one: LatestCoverageEnd reads maintenance windows AND operator silences
// alike, since both are the same maintenance_windows rows distinguished
// only by reason. The snooze reason needs no fold here at all: its own
// end is already the plain snoozed_until column ChainsNeedingAttentionBase
// reads directly.
//
// Over an interval, not at sampled instants (backlog 4.41). The fold used
// to ask "what covers this instant" at the anchor and at anchor+delay,
// and so never saw an occurrence that opened and closed strictly between
// two ticks without covering either instant; the rung then fired early.
// Nothing records the previous tick — Run's ticker keeps no state, Tick
// takes only `now`, and nothing in the schema stores a per-organization
// tick time — and nothing needs to: the chain's own anchor (the previous
// rung's fired_at, or the latest urgent event or snooze end, all already
// stored) is no later than any previous tick that could matter, so
// [anchor, now] contains every gap between ticks since then. The fold is
// also stateless this way: a window between ticks T1 and T2 still holds
// the rung at T3, which a fold over [T2, T3] alone would not.
//
// Only a chain that is already due by its plain anchor is folded at all.
// The fold can only move the anchor later, so it can turn "due" into "not
// yet" but never the reverse: a chain that is not due pays no read. For a
// due chain, now - delay is at or after the plain anchor, and the read
// covers exactly (now - delay, now]: an occurrence ending at or before
// now - delay leaves end + delay <= now, so it could not hold the rung
// either. The walk is therefore one rung's delay long, and only on the
// ticks between a rung coming naively due and actually firing.
func (f *Follower) foldMaintenanceAnchor(ctx context.Context, orgID, hostID uuid.UUID, cs ChainState, now time.Time) (ChainState, error) {
	step, due := DueStep(cs, now)
	if !due {
		return cs, nil
	}
	from := now.Add(-step.Delay)
	end, ok, err := f.mutes.LatestCoverageEnd(ctx, orgID, hostID, from, now)
	if err != nil {
		return cs, fmt.Errorf("maintenance coverage for host %s between %s and %s: %w", hostID, from, now, err)
	}
	// An end exactly at now counts: the window no longer mutes at its own
	// end instant (half-open), but that end is the anchor all the same.
	if ok && end.After(cs.LastUrgentAt) {
		cs.LastUrgentAt = end
	}
	return cs, nil
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
		URL:          base + "/alerts?host=" + d.alert.Subject.HostID.String(),
		NotSeenSince: alerts.NotSeenSince(d.alert),
		// The alert's latest detail (backlog 3.13b): an escalation is
		// about the alert as it stands, not about one of its events, and
		// the escalated event RecordEscalation writes stores none.
		Detail: d.alert.Detail,
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
