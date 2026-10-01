// This file is the state machine: one pure function that looks at one
// host's measured values and its effective rules and decides what every
// metric's state is. It is the most consequential surface of v0.3b-2 —
// a mistake here is invisible until an operator sees a host that is the
// wrong colour, or does not see one that should be red.
//
// Everything interesting about it is the interaction between hysteresis,
// for_duration and unknown, and the order those are applied in is the
// design, not an implementation detail (the spec's "Order of decision, per
// tuple"). That is why Evaluate is pure: no IO, no clock of its own,
// Snapshot.Now is the only present moment there is, and every interaction
// is a row in evaluate_test.go's table rather than something only a
// running system could show.
package rules

import (
	"time"

	"github.com/google/uuid"
)

const (
	// stalenessFactor is how many check intervals may pass before the last
	// value stops being evidence of anything (spec, step 1).
	stalenessFactor = 3
	// heartbeat is how long a host with nothing pending waits before the
	// ticker looks at it again. A quiet host still gets a cheap heartbeat,
	// which is also what eventually brings a silent agent's tuples into
	// unknown: nothing marks such a host dirty, so only a due time
	// returns to it.
	heartbeat = 15 * time.Second
	// The flapping flag's own hysteresis (spec, step 6): set at this many
	// changes in the counted window, cleared below the lower bound, and
	// carried over in between, so the flag does not itself flap.
	flappingSet   = 5
	flappingClear = 3
)

// ItemKey groups the metrics that share an item, because a rule may
// compare siblings: disk.free_bytes and disk.used_pct of the same mount
// are two tuples but one Input.
type ItemKey struct {
	CheckKey string
	Item     string
}

// TupleSnapshot is one tuple as the agent last reported it: its value or
// label, the collection error if there was one, when it was measured, and
// the resolved interval of the check that measured it — which is what
// "stale" is defined against.
type TupleSnapshot struct {
	Tuple    Tuple
	Value    *float64
	Label    *string
	Err      string        // collection error reported by the agent
	At       time.Time     // when the agent measured it
	Interval time.Duration // the check's resolved interval
}

// WindowRequest is one window aggregate to fetch, addressed by item rather
// than by metric: a rule tree's WindowKey says which metric and which
// aggregate, and the tuple being judged says which item that applies to.
type WindowRequest struct {
	Key     ItemKey
	Metric  string
	Agg     string
	Seconds int
}

// Snapshot is everything one host's evaluation reads. Now is the only
// clock: nothing below calls time.Now.
//
// Previous is the state of every tuple as the last evaluation left it; a
// tuple absent from it has never been evaluated. Changes30 is how often
// each tuple changed inside the flapping window as the caller loaded it —
// the changes this evaluation is about to write are added on top of it
// here, so a tuple whose fifth change is the one happening right now is
// flagged on this tick rather than the next one.
type Snapshot struct {
	Now       time.Time
	Tuples    []TupleSnapshot
	Effective []Effective
	Windows   map[WindowRequest]float64
	Facts     map[string]string
	Previous  map[Tuple]TupleState
	Changes30 map[Tuple]int
	// PreviousHost is the roll-up the last evaluation wrote, and the only
	// thing Host.Since can be carried from — the tuple states alone cannot
	// say when the roll-up last changed value, only when each tuple did.
	//
	// It is a pointer, and nil is a supported input, not an oversight: a
	// caller that does not supply it gets the derivation rollUp applies
	// instead (see that function), which is accurate except when one
	// alerting tuple recovers while another keeps the host at the same
	// state — then Since drifts forward to the surviving tuple. Degrading
	// to that is deliberate. The alternative, treating a missing roll-up
	// as "the state changed", would reset Since on every single tick,
	// which is a far worse failure than a bounded drift.
	PreviousHost *HostState
}

// Result is what one evaluation concluded: the new state of every tuple in
// the snapshot (in the snapshot's own order), the transitions to append to
// the history, the host roll-up, and when the host should be looked at
// again.
//
// Host is the zero HostState when the evaluation judged no tuple at all —
// either because the snapshot carried none, or because every one it
// carried was left out for want of a resolved interval (see the skip in
// Evaluate). A host with no tuples gets no roll-up row and renders as "no
// data" (the spec's step 7), so a zero Host is an instruction to the
// caller: write no row, and delete any check_host_states row the host
// still has. A roll-up is derived state — the history it was derived from
// lives in check_state_changes and is untouched — so leaving a stale one
// behind would hold the host at whatever colour it last was for ever,
// which is the very failure the interval exclusion exists to avoid.
// Host.HostID is left zero: the snapshot does not name the host, and the
// caller that loaded it already knows which one it is.
type Result struct {
	States  []TupleState
	Changes []Change
	Host    HostState
	NextDue time.Time
}

// WindowRequests is what the store must fetch before Evaluate can run: one
// request per (item, window) that a winning rule's trigger or clear
// condition names, deduplicated, in the order they are first needed.
//
// It reads only Tuples and Effective, so the ticker calls it on a Snapshot
// whose Windows field is still nil and fills that field from the result.
// Only rules judging a tuple Evaluate will actually judge contribute — one
// the host reports and whose check still has a resolved interval — since a
// window for anything else would be fetched and never read.
func WindowRequests(s Snapshot) []WindowRequest {
	reported := make(map[Tuple]bool, len(s.Tuples))
	for _, ts := range s.Tuples {
		if measured(ts) {
			reported[ts.Tuple] = true
		}
	}

	seen := make(map[WindowRequest]bool)
	var out []WindowRequest
	collect := func(key ItemKey, n Node) {
		for _, wk := range WindowKeys(n) {
			req := WindowRequest{Key: key, Metric: wk.Metric, Agg: wk.Agg, Seconds: wk.Seconds}
			if !seen[req] {
				seen[req] = true
				out = append(out, req)
			}
		}
	}
	for _, e := range s.Effective {
		if !reported[e.Tuple] {
			continue
		}
		key := itemKeyOf(e.Tuple)
		// Both trees: hysteresis evaluates Clear, so its windows have to be
		// on hand too. WindowKeys of the zero Node is empty, which is what
		// a rule with no clear condition carries.
		collect(key, e.Winner.Trigger)
		collect(key, e.Winner.Clear)
	}
	return out
}

// Evaluate decides every tuple's state. Pure: no IO, no clock of its own,
// and nothing in s is mutated. The steps below are the spec's "Order of
// decision, per tuple" in that exact order — the order is the design, so
// each step names the reason it sits where it does.
func Evaluate(s Snapshot) Result {
	inputs := buildInputs(s)
	byTuple := rulesPerTuple(s.Effective)

	var (
		states  []TupleState
		changes []Change
		// pending collects every moment this host has a reason to be looked
		// at again: candidate ripenings and staleness deadlines (step 5).
		pending []time.Time
	)

	for _, ts := range s.Tuples {
		if !measured(ts) {
			// A tuple whose check has no resolved interval is one nothing
			// asks anyone to measure any more: its definition is gone, and
			// the value on hand is frozen at whatever it was when it went.
			// It gets no state and no part in the roll-up — exactly as if
			// it were not in Tuples at all. Neither alternative survives
			// contact with an operator: sweeping it to unknown would leave
			// a deleted check holding the host grey on the fleet page for
			// ever with nothing anyone could do to clear it, and judging
			// the frozen value would keep a disk that was 96 % full when
			// its check was deleted at crit on data nobody refreshes. This
			// is v0.3b-1's own ruling for a check_current row that outlives
			// the thing it measured (backlog 3.28) one layer up: the stored
			// row stays exactly as it is — nothing here deletes it — and
			// the read side is what leaves it out.
			continue
		}

		prev, seen := s.Previous[ts.Tuple]
		if !seen {
			// A tuple evaluated for the first time is judged against ok:
			// nothing yet attests that a deterioration held before this
			// very tick, so a for_duration rule still has to ripen.
			prev = TupleState{Tuple: ts.Tuple, State: StateOK, Since: s.Now}
		}

		next, ripensAt := decide(s, ts, inputs[itemKeyOf(ts.Tuple)], byTuple[ts.Tuple], prev)

		// Since marks when the current state started, so it moves only when
		// the state does: a re-confirmed state keeps the moment it began.
		changed := next.State != prev.State
		if changed {
			next.Since = s.Now
		} else {
			next.Since = prev.Since
		}
		if changed && seen {
			// A tuple's first evaluation is an insert, not a transition:
			// there is no earlier state to record a move away from, and
			// check_state_changes holds transitions only (its own
			// CHECK (from_state <> to_state)).
			changes = append(changes, Change{
				At:     s.Now,
				Tuple:  ts.Tuple,
				From:   prev.State,
				To:     next.State,
				RuleID: next.RuleID,
				Value:  ts.Value,
				Label:  ts.Label,
			})
		}

		// Step 6: flapping, with its own hysteresis so that the flag does
		// not itself flap. The count is the loaded window plus the change
		// this evaluation is about to write, so a tuple whose fifth change
		// is the one happening right now is flagged now — the flag is for
		// an operator watching a tuple move, and it should not lag a tick
		// behind the move that earned it.
		count := s.Changes30[ts.Tuple]
		if changed && seen {
			count++
		}
		switch {
		case count >= flappingSet:
			next.Flapping = true
		case count < flappingClear:
			next.Flapping = false
		default:
			next.Flapping = prev.Flapping
		}

		states = append(states, next)
		if ripensAt != nil {
			pending = append(pending, *ripensAt)
		}
		// A staleness deadline already behind us is not pending — the tuple
		// is unknown already and rescheduling into the past would spin the
		// ticker on this host every time it looked at it.
		if deadline := staleAt(ts); deadline.After(s.Now) {
			pending = append(pending, deadline)
		}
	}

	return Result{
		States:  states,
		Changes: changes,
		Host:    rollUp(states, s.Now, s.PreviousHost),
		NextDue: nextDue(pending, s.Now),
	}
}

// decide runs steps 1 to 4 for one tuple: it reports the state to write
// (with the deciding rule, the carried value and any candidate still
// ripening) and, separately, the moment such a candidate would take
// effect — nil when nothing is ripening.
func decide(s Snapshot, ts TupleSnapshot, in Input, bySeverity map[Severity]Rule, prev TupleState) (TupleState, *time.Time) {
	next := TupleState{
		Tuple:       ts.Tuple,
		LastValue:   ts.Value,
		LastLabel:   ts.Label,
		EvaluatedAt: s.Now,
	}

	// Step 1: unknown first, and alone. A rule cannot say anything true
	// about data that is not there, so this outranks every threshold, no
	// clear condition holds a state against it, and a ripening candidate is
	// discarded rather than left to ripen in the dark (review focus 3).
	if ts.Err != "" || s.Now.After(staleAt(ts)) {
		next.State = StateUnknown
		return next, nil
	}

	// Step 2: the effective rules, highest severity first; the first match
	// decides and no match is ok. Resolve has already picked one winner per
	// (tuple, severity), so there is nothing left to tie-break here.
	target, decider, decided := firstMatch(bySeverity, in)

	// Step 3: hysteresis, before any ripening. A tuple standing at warn or
	// crit whose deciding rule carries a clear condition holds that state
	// until the clear predicate is true — not merely until the trigger
	// predicate is false. It holds only against an improvement: an
	// escalation is never something a clear condition should stand in the
	// way of. The rule consulted is the effective one at the state's own
	// severity, so a rule deleted while it held a crit leaves nothing
	// behind to hold it (review focus 7).
	if isAlerting(prev.State) && stateRank(target) < stateRank(prev.State) {
		if holder, ok := bySeverity[severityOf(prev.State)]; ok && holder.Clear.Kind != "" && !Match(holder.Clear, in) {
			target, decider, decided = prev.State, holder, true
		}
	}

	// Step 4: for_duration, downward only. A deterioration has to hold
	// continuously for the rule's duration before it becomes the state; an
	// improvement takes effect at once, because a host that is healthy
	// again must not stay red for another ten minutes.
	if stateRank(target) <= stateRank(prev.State) {
		next.State = target
		next.RuleID = ruleIDOf(decider, decided)
		return next, nil
	}

	// candidate_since is a column, not a variable, so a wait that started
	// before a manager restart carries on from where it was rather than
	// starting over (review focus 4). A candidate aiming at a different
	// state is a different wait, so it starts its own clock — and so is a
	// wait nobody was watching, which is what continuous below is for.
	since := s.Now
	if prev.Candidate == target && prev.CandidateSince != nil && continuous(s, ts, prev) {
		since = *prev.CandidateSince
	}
	// decided is guaranteed here rather than checked: this point is only
	// reached on a strict deterioration, and an undecided target is
	// StateOK, which outranks nothing. So there is always a rule whose
	// duration to ripen against.
	ripensAt := since.Add(decider.For)
	if !s.Now.Before(ripensAt) {
		next.State = target
		next.RuleID = ruleIDOf(decider, decided)
		return next, nil
	}

	// Still ripening: the state does not move, and neither does the rule
	// that holds it — so nothing is written to the history either. The
	// history records states, not intentions, and check_state_changes is
	// what the flapping detector counts, so a candidate that flips away
	// before it ripens must leave no trace at all.
	next.State = prev.State
	next.RuleID = prev.RuleID
	next.Candidate = target
	next.CandidateSince = &since
	return next, &ripensAt
}

// continuous reports whether the previous evaluation of ts is recent
// enough for a candidate it recorded to keep counting. A for_duration wait
// is a claim that a deterioration HELD for that long, so it may only be
// carried across a gap short enough that the tuple was actually being
// watched through it — the same three intervals staleness itself uses.
//
// What this exists for is the one gap the staleness path cannot heal: a
// tuple whose check stops resolving an interval is skipped entirely by
// Evaluate, so its check_states row freezes exactly as it was, candidate
// and all. Disable a disk check at 09:01 with a 10-minute crit candidate
// set at 09:00, re-enable it three days later with the value still high,
// and without this guard the first tick computes a ripening moment three
// days in the past and promotes to crit at once — skipping the whole wait
// on a deterioration never observed to hold, and then reporting "crit
// since three days ago" for a span in which nothing was measured. A
// manager restart does not reach this, because the staleness deadline
// fires first and step 1 discards the candidate on its way through
// unknown; a disabled check never runs that path at all.
func continuous(s Snapshot, ts TupleSnapshot, prev TupleState) bool {
	return !s.Now.After(prev.EvaluatedAt.Add(stalenessFactor * ts.Interval))
}

// buildInputs builds one Input per ItemKey rather than per tuple: a rule on
// disk.used_pct may legitimately read disk.free_bytes of the same mount, so
// the sibling metrics of one item have to be visible to each other. Window
// values are translated the other way round at the same time — the store
// fetched them per item, and a tree asks for them per metric.
//
// A value is exposed whether or not its own tuple is stale or errored: the
// metrics of one item are collected together, so a sibling that is stale
// makes the tuple reading it stale too, and step 1 has that covered before
// any rule is evaluated. A tuple left out of the evaluation is left out
// here too — "as if it were not in Tuples" includes not being readable by
// its siblings.
func buildInputs(s Snapshot) map[ItemKey]Input {
	inputs := make(map[ItemKey]Input)
	inputFor := func(key ItemKey) Input {
		in, ok := inputs[key]
		if !ok {
			in = Input{
				Metrics: map[string]float64{},
				Labels:  map[string]string{},
				Windows: map[WindowKey]float64{},
				Facts:   s.Facts,
			}
			inputs[key] = in
		}
		return in
	}
	for _, ts := range s.Tuples {
		if !measured(ts) {
			continue // left out of the evaluation entirely; see Evaluate
		}
		in := inputFor(itemKeyOf(ts.Tuple))
		if ts.Value != nil {
			in.Metrics[ts.Tuple.Metric] = *ts.Value
		}
		if ts.Label != nil {
			in.Labels[ts.Tuple.Metric] = *ts.Label
		}
	}
	for req, v := range s.Windows {
		inputFor(req.Key).Windows[WindowKey{Metric: req.Metric, Agg: req.Agg, Seconds: req.Seconds}] = v
	}
	return inputs
}

// rulesPerTuple indexes the effective view by tuple and severity, which is
// the shape step 2 reads it in. Resolve guarantees at most one winner per
// (tuple, severity), so nothing is lost by keying on the pair.
func rulesPerTuple(effective []Effective) map[Tuple]map[Severity]Rule {
	out := make(map[Tuple]map[Severity]Rule)
	for _, e := range effective {
		bySeverity, ok := out[e.Tuple]
		if !ok {
			bySeverity = make(map[Severity]Rule, 2)
			out[e.Tuple] = bySeverity
		}
		bySeverity[e.Severity] = e.Winner
	}
	return out
}

// firstMatch is step 2: crit before warn, the first match decides, no match
// is ok. The order is severity's, never the order the effective rules
// happened to arrive in.
func firstMatch(bySeverity map[Severity]Rule, in Input) (State, Rule, bool) {
	for _, sev := range []Severity{SeverityCrit, SeverityWarn} {
		r, ok := bySeverity[sev]
		if !ok {
			continue
		}
		if Match(r.Trigger, in) {
			return stateOf(sev), r, true
		}
	}
	return StateOK, Rule{}, false
}

// measured reports whether ts belongs to a check the manager still resolves
// an interval for. A tuple that fails this is left out of the evaluation
// entirely — see the skip in Evaluate for why, and note that both staleAt
// and step 1 are only meaningful once it holds.
func measured(ts TupleSnapshot) bool { return ts.Interval > 0 }

// staleAt is the moment ts stops being evidence of anything: three of its
// check's intervals after it was measured. Only meaningful for a tuple
// measured reports true for.
func staleAt(ts TupleSnapshot) time.Time {
	return ts.At.Add(stalenessFactor * ts.Interval)
}

// nextDue is step 5: the earliest moment this host has a reason to be
// looked at again, or a cheap heartbeat when it has none. That fallback is
// what makes unknown reachable at all — a host whose agent has died sends
// nothing, so nothing marks it dirty and only a due time brings the ticker
// back to it.
func nextDue(pending []time.Time, now time.Time) time.Time {
	if len(pending) == 0 {
		return now.Add(heartbeat)
	}
	earliest := pending[0]
	for _, t := range pending[1:] {
		if t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// rollUp is step 7: the host's worst tuple state with a count per severity.
// No judged tuples yields the zero HostState, which is how Evaluate says
// "write no row, and delete the one that is there" — see Result's own doc
// comment for why deleting rather than leaving it is the right reading. A
// supplied previous roll-up does not change that: there is nothing to
// carry a Since onto.
//
// Since carries over from previous while the host state is unchanged, and
// is now when it changed. When previous is nil — a caller that does not
// supply the last roll-up, which Snapshot.PreviousHost documents as a
// supported input — it is derived from the tuple states instead: for an
// alerting host the earliest moment a tuple still at that state entered it,
// since the host has demonstrably stood there ever since; for a healthy
// host the latest of every tuple's Since, since a host is only healthy from
// the moment its last unhealthy tuple recovered, not from the oldest tuple
// that happened to be ok all along.
func rollUp(states []TupleState, now time.Time, previous *HostState) HostState {
	if len(states) == 0 {
		return HostState{}
	}
	host := HostState{State: StateOK, EvaluatedAt: now}
	for _, st := range states {
		host.State = Worse(host.State, st.State)
		switch st.State {
		case StateCrit:
			host.Crit++
		case StateWarn:
			host.Warn++
		case StateUnknown:
			host.Unknown++
		case StateOK:
		}
	}
	if previous != nil {
		if previous.State == host.State {
			host.Since = previous.Since
		} else {
			host.Since = now
		}
		return host
	}
	first := true
	for _, st := range states {
		if st.State != host.State {
			continue
		}
		switch {
		case first:
			host.Since = st.Since
			first = false
		case host.State == StateOK && st.Since.After(host.Since):
			host.Since = st.Since
		case host.State != StateOK && st.Since.Before(host.Since):
			host.Since = st.Since
		}
	}
	return host
}

func itemKeyOf(t Tuple) ItemKey { return ItemKey{CheckKey: t.CheckKey, Item: t.Item} }

// isAlerting reports whether s is a state a clear condition can hold.
func isAlerting(s State) bool { return s == StateWarn || s == StateCrit }

// stateOf and severityOf are the two halves of the one-to-one mapping
// between the severity a rule judges towards and the state it produces.
func stateOf(sev Severity) State {
	if sev == SeverityCrit {
		return StateCrit
	}
	return StateWarn
}

func severityOf(s State) Severity {
	if s == StateCrit {
		return SeverityCrit
	}
	return SeverityWarn
}

// ruleIDOf returns the id of the rule that decided a state, or nil when no
// rule did — ok and unknown are both states nothing judged towards.
func ruleIDOf(r Rule, decided bool) *uuid.UUID {
	if !decided {
		return nil
	}
	id := r.ID
	return &id
}
