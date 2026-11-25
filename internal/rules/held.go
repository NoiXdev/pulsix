package rules

// Held tuples (backlog 3.9b ruling). A tuple its descriptor holds on its
// item's state (TupleSnapshot.Held: a sleeping disk's sector count, an
// exported pool's capacity) is not judged: the agent reports nothing but
// the item's state while the item is in it, so the tuple's last reading
// is not stale and its stored state is still the last thing known about
// it. It is handled like a frozen client (backlog 3.57), one tuple at a
// time: no judgement, no write, not Unmeasured — its alert must neither
// close nor move — and its stored state stands in the host's roll-up as
// it is. When the item leaves the state, the tuple is measured again and
// judged on that same stored state — no fresh start (backlog 4.42), since
// nothing went unmeasured — except that a for_duration wait does not
// carry across the hold (TupleSnapshot.Resumed): the first new reading
// decides.

// heldFate is what becomes of a held tuple this evaluation.
type heldFate int

const (
	// heldNothing: nothing to hold — no stored state, a row marked
	// unmeasured (no state to resume, backlog 4.42), or a stored unknown,
	// which is nothing known: its reading went stale or failed before the
	// item entered its state, the staleness a held tuple is exempt from.
	// The row, if any, stands unwritten and keeps no host unknown.
	heldNothing heldFate = iota
	// heldDropped: an operator's ignore governs the item whatever its
	// state; the row is dropped as any ignored tuple's is.
	heldDropped
	// heldStanding: the stored state stands, unwritten, in the roll-up.
	heldStanding
	// heldResolved: the rule that judged the stored warn or crit no longer
	// applies — deleted, disabled, or no longer the tuple's winner — so
	// nothing holds it there any more: it is judged ok on no rule, as an
	// awake tuple whose rule went would be (resolveHeld), and holds that ok
	// from then on.
	heldResolved
)

// heldFateOf decides a held tuple's fate from its stored state and the
// rules in force for it now.
func heldFateOf(s Snapshot, ts TupleSnapshot, bySeverity map[Severity]Rule) heldFate {
	prev, seen := s.Previous[ts.Tuple]
	switch {
	case !seen:
		return heldNothing
	case s.Ignored[itemKeyOf(ts.Tuple)]:
		return heldDropped
	case prev.UnmeasuredSinceJudged() || prev.State == StateUnknown:
		return heldNothing
	case isAlerting(prev.State) && !judgedByOneOf(prev, bySeverity):
		return heldResolved
	}
	return heldStanding
}

// judgedByOneOf reports whether the rule that put prev in its state is
// still one of the tuple's winning rules. A row whose rule was deleted
// carries none (check_states.rule_id is set to NULL with it).
func judgedByOneOf(prev TupleState, bySeverity map[Severity]Rule) bool {
	if prev.RuleID == nil {
		return false
	}
	for _, r := range bySeverity {
		if r.ID == *prev.RuleID {
			return true
		}
	}
	return false
}

// resolveHeld is a held tuple's state once the rule that judged it is
// gone: ok, on no rule, with its last reading.
func resolveHeld(s Snapshot, ts TupleSnapshot) TupleState {
	return TupleState{
		Tuple: ts.Tuple, State: StateOK,
		LastValue: ts.Value, LastLabel: ts.Label, EvaluatedAt: s.Now,
	}
}

// withoutCandidate is prev for a tuple back from a hold: the first new
// reading decides, so a for_duration wait that was ripening when the item
// entered its state does not carry across it — whatever its length, which
// the staleness window `continuous` measures cannot tell from a late round.
func withoutCandidate(prev TupleState) TupleState {
	prev.Candidate, prev.CandidateSince = "", nil
	return prev
}

// markResumed sets TupleSnapshot.Resumed on every tuple asked now that its
// item's previous state held: the labels the item read at the last
// evaluation, from its tuples' stored states, would have held it. The
// state metric is always asked, so its row says what the item read then.
func markResumed(tuples []TupleSnapshot, previous map[Tuple]TupleState, checkCtx CheckContext) {
	before := map[ItemKey]map[string]string{}
	for t, st := range previous {
		if st.LastLabel == nil {
			continue
		}
		k := itemKeyOf(t)
		if before[k] == nil {
			before[k] = map[string]string{}
		}
		before[k][t.Metric] = *st.LastLabel
	}
	for i := range tuples {
		ts := &tuples[i]
		if ts.Interval > 0 && before[itemKeyOf(ts.Tuple)] != nil {
			ts.Resumed = checkCtx.held(ts.Tuple, before[itemKeyOf(ts.Tuple)])
		}
	}
}
