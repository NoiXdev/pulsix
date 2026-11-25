// Reconcile is the alert lifecycle's brain: a pure function, no clock and
// no database, that looks at one host's post-evaluation states and the
// alerts currently open on it and says what must change. The design doc's
// "Lifecycle" section (docs/superpowers/specs/2026-10-06-v0.4a-alerts-design.md)
// is the spec this translates directly; the store (Task 5) is the only
// thing that executes what it decides, inside the evaluator's own
// transaction.
//
// Determinism matters here the way it mattered for rules.Resolve in
// v0.3b-2: Reconcile iterates in.States in the order it was given and
// never ranges over the map it builds from open, so the returned []Action
// is a function of the input alone, never of Go's map iteration order.
package alerts

import (
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// Reconcile decides, for one host's evaluation, what must change about its
// alerts. It takes no now: every timestamp on the returned Actions comes
// from in — a tuple state's own Since for an opening only, so that a slow
// ticker cannot move an alert's history forward; in.At for everything
// else, since a severity change, a flap latching or a close are each
// dated by when this evaluation observed them, not by when the underlying
// state happened to begin.
//
// closed is the host's recently closed tuple episodes, the latest per
// subject (Store.RecentlyClosedForHost): an opening whose subject has one
// may take over its acknowledgement and snooze (adoption, backlog 4.39).
// Like open, it is only ever looked up, never ranged over.
func Reconcile(in rules.AlertInput, open, closed []Alert) []Action {
	bySubject := make(map[Subject]Alert, len(open))
	for _, a := range open {
		bySubject[a.Subject] = a
	}
	previous := make(map[Subject]Alert, len(closed))
	for _, a := range closed {
		previous[a.Subject] = a
	}

	var actions []Action
	for _, ts := range in.States {
		if ts.State == rules.StateUnknown {
			// Decision 4 / lifecycle 2a: unknown is the host's business
			// alone. A tuple reading unknown must not close an alert (it
			// is not ok) and must not become the alert's severity either
			// (that would let a dying agent quietly downgrade a crit).
			continue
		}

		sub := Subject{
			HostID:   in.HostID,
			Scope:    ScopeTuple,
			CheckKey: ts.Tuple.CheckKey,
			Item:     ts.Tuple.Item,
			Metric:   ts.Tuple.Metric,
		}
		existing, found := bySubject[sub]

		if ts.State == rules.StateOK {
			// `found` is read here, not inside closeOrFlap (backlog
			// 4.20), so both branches of this switch ask the same
			// question in the same place: an ok tuple with no open
			// alert is nothing to do, exactly as a warn one with no
			// open alert is an open.
			if found {
				actions = append(actions, closeOrFlap(in.At, ts, existing)...)
			}
			continue
		}

		// ts.State is warn or crit from here on.
		if !found {
			prev, hasPrev := previous[sub]
			actions = append(actions, Action{
				Kind: ActionOpen, Subject: sub, At: ts.Since, Severity: ts.State,
				Adopt: adoption(in.At, ts.State, prev, hasPrev),
			})
			continue
		}
		actions = append(actions, advance(in.At, ts, existing)...)
	}

	actions = append(actions, staleActions(in, bySubject)...)
	actions = append(actions, hostAction(in, bySubject)...)
	return actions
}

// reopenAdoptWindow is how long after a `stale` close a re-opening of the
// same subject still continues the closed episode (backlog 4.39, owner
// decision 2026-11-25): a check switched off and on again, a host
// un-monitored for a moment, a selector edited and corrected.
const reopenAdoptWindow = time.Hour

// reopenAdoptSince is the earliest close a re-opening at `at` can adopt
// from: the lower bound Service.Apply loads the closed episodes with.
func reopenAdoptSince(at time.Time) time.Time { return at.Add(-reopenAdoptWindow) }

// adoption decides what a tuple alert opening at severity, in the
// evaluation dated at, takes over from prev, its subject's latest closed
// episode (backlog 4.39). It returns nil — a plain opening — unless:
//
//   - prev closed `stale` (a vanished subject; `resolved`, `forgotten` and
//     any other reason end the story) and no earlier than one hour before
//     at — exactly one hour is still inside;
//   - the new severity is not above prev's acknowledged severity. When it
//     is, nothing at all is adopted, the snooze included (owner decision):
//     the problem came back worse than anyone said they knew about.
//
// Within those, the acknowledgement is adopted when prev was acknowledged
// (a voided acknowledgement is not one), as it stood; the snooze when it
// is a timed one still running at at, or one until the severity changes
// and the new severity equals prev's severity at its close. With neither
// to adopt there is nothing to continue, and the opening is plain. Only
// tuple subjects get here: the host-scope alert opens in hostAction,
// which never adopts. Peak is never adopted.
func adoption(at time.Time, severity rules.State, prev Alert, found bool) *Adoption {
	if !found || prev.ClosedReason != Stale || prev.ClosedAt == nil {
		return nil
	}
	if prev.ClosedAt.Before(reopenAdoptSince(at)) || prev.ClosedAt.After(at) {
		return nil
	}
	if prev.AckSeverity != "" && rules.StateRank(severity) > rules.StateRank(prev.AckSeverity) {
		return nil
	}
	ad := Adoption{From: prev.ID}
	if prev.AckSeverity != "" {
		ad.AckAt, ad.AckBy, ad.AckSeverity, ad.AckNote = prev.AckAt, prev.AckBy, prev.AckSeverity, prev.AckNote
	}
	switch {
	case prev.SnoozedUntil != nil && prev.SnoozedUntil.After(at):
		ad.SnoozedUntil = prev.SnoozedUntil
	case prev.SnoozeUntilChange && severity == prev.Severity:
		ad.SnoozeUntilChange = true
	}
	if !ad.Ack() && !ad.Snooze() {
		return nil
	}
	return &ad
}

// staleActions is design decision 5's "the check is taken off the host or
// disabled" path, and the reason it is here rather than in the sweep: the
// evaluator leaves an unmeasured tuple's check_states row standing on
// purpose (rules.Result.Unmeasured's own doc comment), so the sweep's
// "no state row exists" test — which catches the item-dropped path — can
// never see this one. The subject has nonetheless gone: nothing measures
// the tuple any more, nothing will ever judge it ok, and `stale` is the
// honest close for it, the same word the sweep uses for the same fact.
//
// It runs over in.Unmeasured, never over the open alerts, so the result
// stays a function of the input's own order (this file's own doc
// comment). Closing an alert that is not open is impossible by
// construction: bySubject only holds open ones.
func staleActions(in rules.AlertInput, bySubject map[Subject]Alert) []Action {
	var out []Action
	for _, t := range in.Unmeasured {
		sub := Subject{
			HostID:   in.HostID,
			Scope:    ScopeTuple,
			CheckKey: t.CheckKey,
			Item:     t.Item,
			Metric:   t.Metric,
		}
		if existing, found := bySubject[sub]; found {
			out = append(out, Action{Kind: ActionClose, AlertID: existing.ID, At: in.At, Reason: Stale})
		}
	}
	return out
}

// closeOrFlap is lifecycle step 2 and decision 6: an open alert whose
// tuple reads ok closes as resolved, unless the tuple is still flapping —
// decision 1 on the time axis instead of the severity axis. A flapping
// tuple not yet marked as such latches was_flapping instead of closing, so
// the episode remembers it flapped even once it finally settles.
// It is only ever called for a tuple that HAS an open alert; the caller
// checks that, next to the same check for the warn/crit path.
func closeOrFlap(at time.Time, ts rules.TupleState, existing Alert) []Action {
	if !ts.Flapping {
		return []Action{{Kind: ActionClose, AlertID: existing.ID, At: at, Reason: Resolved}}
	}
	if !existing.WasFlapping {
		return []Action{{Kind: ActionFlapped, AlertID: existing.ID, At: at}}
	}
	return nil
}

// advance is lifecycle step 1 for an alert that already exists: its
// severity moves (decision 2 voids the acknowledgement on a worsening
// past acknowledged_severity, and clears any snoozed_until_change in
// either direction since that mute is otherwise unbounded), and its
// was_flapping flag latches once the tuple flaps — independently, since a
// tuple can start flapping on a tick where its severity does not change.
func advance(at time.Time, ts rules.TupleState, existing Alert) []Action {
	var out []Action
	if existing.Severity != ts.State {
		voidsAck := existing.AckSeverity != "" && rules.StateRank(ts.State) > rules.StateRank(existing.AckSeverity)
		out = append(out, Action{
			Kind:                    ActionSeverity,
			AlertID:                 existing.ID,
			At:                      at,
			Severity:                ts.State,
			VoidsAck:                voidsAck,
			ClearsSnoozeUntilChange: existing.SnoozeUntilChange,
		})
	}
	if ts.Flapping && !existing.WasFlapping {
		out = append(out, Action{Kind: ActionFlapped, AlertID: existing.ID, At: at})
	}
	return out
}

// hostAction is lifecycle step 3 / decision 4: the host roll-up, not any
// tuple, is what opens or closes the one scope-host alert. It exists only
// while the roll-up itself reads unknown; any other roll-up state closes
// it, the mirror image of a tuple alert closing on ok.
//
// The close reason distinguishes two of those "any other" states that are
// not the same fact at all (v0.4a final review, Critical, the host half of
// decision 5): a roll-up that reads ok/warn/crit says the host was judged
// and is no longer unknown — resolved. The ZERO HostState says the host
// was not judged at all, because every check it had has been taken off it
// (rules.Result.Host's own doc comment: "write no row, and delete the one
// that is there"). That is a vanished subject, not a recovery, so it
// closes `stale` for exactly the reason decision 5 gives: "closing those
// as resolved would be a lie in the one table whose job is the record".
//
// Client hosts (backlog 3.57) put a second alert on the same subject: the
// absent warning, severity warn, for a frozen client whose agent has not
// been seen for the organization's threshold. Severity is the
// discriminator — the host-scope alert is otherwise only ever unknown —
// and the subject never holds both: hostWants decides which one (if any)
// this evaluation wants, an open alert of the other kind is closed first,
// and only then is the wanted one opened, so the one-open-per-subject
// index never sees two. A server has no presence (never frozen, never
// absent, no grace) and takes exactly the path it took before.
func hostAction(in rules.AlertInput, bySubject map[Subject]Alert) []Action {
	sub := Subject{HostID: in.HostID, Scope: ScopeHost}
	existing, found := bySubject[sub]
	want := hostWants(in, found, existing)

	var out []Action
	if found {
		if existing.Severity == want {
			return nil
		}
		out = append(out, Action{Kind: ActionClose, AlertID: existing.ID, At: in.At, Reason: hostCloseReason(in, existing)})
	}
	switch want {
	case rules.StateUnknown:
		out = append(out, Action{Kind: ActionOpen, Subject: sub, At: in.Host.Since, Severity: rules.StateUnknown})
	case rules.StateWarn:
		// Dated at the tick that found it, not at the threshold's
		// crossing: a lowered threshold (or a long-gone server switched to
		// a client) would otherwise open it days in the past, under a
		// setting that did not exist then. The historical fact — since
		// when the client has not been seen — is the agent's last
		// contact, which the warning shows on its own (NotSeenSince).
		out = append(out, Action{Kind: ActionOpen, Subject: sub, At: in.At, Severity: rules.StateWarn})
	}
	return out
}

// hostWants is the severity the host-scope alert should have after this
// evaluation, or "" for none:
//
//   - A frozen client (spec item 1) never opens the unknown alert — its
//     carried roll-up is not a verdict — and wants the absent warning
//     exactly while it is absent (spec item 3). An unknown alert already
//     open stays, as every open alert of a frozen client does, unless
//     its absence caused it (absenceCaused); once due, the absent warning
//     takes the subject from it either way.
//   - Any other host wants the unknown alert while its roll-up reads
//     unknown. A client with an agent waits out the grace first, counted
//     from the later of the roll-up's start and its online period's
//     (UnknownGraceStart): a client that is shutting down is frozen before
//     its alert would open, and one that has just come back with a
//     roll-up carried unknown through the freeze gets the same grace. One
//     that already holds the unknown alert keeps it for as long as the
//     roll-up stays unknown.
func hostWants(in rules.AlertInput, found bool, existing Alert) rules.State {
	if in.Frozen {
		if in.Absent {
			return rules.StateWarn
		}
		if found && existing.Severity == rules.StateUnknown && !absenceCaused(in) {
			return rules.StateUnknown
		}
		return ""
	}
	if in.Host.State != rules.StateUnknown {
		return ""
	}
	if found && existing.Severity == rules.StateUnknown {
		return rules.StateUnknown
	}
	if in.UnknownGrace > 0 && in.At.Before(in.UnknownGraceStart().Add(in.UnknownGrace)) {
		return ""
	}
	return rules.StateUnknown
}

// absenceCaused reports whether a frozen client's unknown roll-up is the
// absence itself rather than a problem the client had while it was on:
// it went unknown at or after the agent's last contact. That is the
// server switched to a client while its agent was off, and a shutdown
// that slipped past the grace; such an alert closes stale. An unknown
// that began before the last contact was a real problem while the client
// was on, and stays open through the freeze — no close, no notification,
// its acknowledgement and any escalation untouched — so a client with a
// genuine unknown does not close and reopen its alert at every power
// cycle. A certificate renewal reads the agent as last seen at its
// registration, which is later still, so it keeps the alert too. A
// carried roll-up that no longer reads unknown has nothing to keep the
// alert for.
func absenceCaused(in rules.AlertInput) bool {
	return in.Host.State != rules.StateUnknown || !in.Host.Since.Before(in.AgentLastSeen)
}

// hostCloseReason is why an open host-scope alert this evaluation no
// longer wants closes.
//
//   - The unknown alert of a frozen client closes stale — its absence
//     caused it (a server switched to a client while its agent was off, a
//     shutdown that slipped past the grace) or the absent warning takes
//     the subject; nothing says it recovered.
//   - The absent warning closes resolved only when the client is back: not
//     frozen, and still a client with an agent (ClientWithAgent). While
//     frozen it closes because the threshold was raised or the warning
//     switched off, and on a host that is no longer a client with an
//     agent because there is nothing left to be absent; neither is a
//     return, so both are stale.
//   - Anything else is the pre-3.57 rule: resolved, or stale for the zero
//     roll-up of a host nothing judges any more.
func hostCloseReason(in rules.AlertInput, existing Alert) CloseReason {
	if existing.Severity == rules.StateWarn {
		if !in.Frozen && in.ClientWithAgent {
			return Resolved
		}
		return Stale
	}
	if in.Frozen || in.Host.State == "" {
		return Stale
	}
	return Resolved
}
