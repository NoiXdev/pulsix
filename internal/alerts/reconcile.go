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
func Reconcile(in rules.AlertInput, open []Alert) []Action {
	bySubject := make(map[Subject]Alert, len(open))
	for _, a := range open {
		bySubject[a.Subject] = a
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
			actions = append(actions, closeOrFlap(in.At, ts, existing, found)...)
			continue
		}

		// ts.State is warn or crit from here on.
		if !found {
			actions = append(actions, Action{Kind: ActionOpen, Subject: sub, At: ts.Since, Severity: ts.State})
			continue
		}
		actions = append(actions, advance(in.At, ts, existing)...)
	}

	actions = append(actions, hostAction(in, bySubject)...)
	return actions
}

// closeOrFlap is lifecycle step 2 and decision 6: an open alert whose
// tuple reads ok closes as resolved, unless the tuple is still flapping —
// decision 1 on the time axis instead of the severity axis. A flapping
// tuple not yet marked as such latches was_flapping instead of closing, so
// the episode remembers it flapped even once it finally settles.
func closeOrFlap(at time.Time, ts rules.TupleState, existing Alert, found bool) []Action {
	if !found {
		return nil
	}
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
func hostAction(in rules.AlertInput, bySubject map[Subject]Alert) []Action {
	sub := Subject{HostID: in.HostID, Scope: ScopeHost}
	existing, found := bySubject[sub]

	if in.Host.State == rules.StateUnknown {
		if found {
			return nil
		}
		return []Action{{Kind: ActionOpen, Subject: sub, At: in.Host.Since, Severity: rules.StateUnknown}}
	}
	if !found {
		return nil
	}
	return []Action{{Kind: ActionClose, AlertID: existing.ID, At: in.At, Reason: Resolved}}
}
