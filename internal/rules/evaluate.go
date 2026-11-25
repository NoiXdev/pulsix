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
	"slices"
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
	// silentHeartbeat replaces heartbeat for a host that has gone fully
	// silent: every tuple already unknown, nothing ripening, nothing
	// changed this pass (backlog 3.37). At 15 s such a host rewrote every
	// state row four times a minute with only evaluated_at moving, and —
	// because ClaimDueEvalCursors orders by next_due_at — the oldest due
	// times belong precisely to those hosts, so a fleet carrying a hundred
	// decommissioned ones took the batch ahead of live hosts, every tick.
	//
	// Backing off costs nothing it was buying. The heartbeat exists to
	// bring a silent host INTO unknown; once it is there, each pass
	// re-derives the same answer from the same absence of data. And it
	// cannot delay a recovery: a sample arriving marks the host dirty, and
	// the claim takes `dirty_at > evaluated_at OR next_due_at <= now()` —
	// the due time is not what brings a returning agent back.
	silentHeartbeat = 5 * time.Minute
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
	// Held marks a tuple its check does not ask for only because of its
	// item's current state (checkplugins.HeldByItemState: a sleeping
	// disk's other metrics, an exported pool's): its Interval is zero like
	// any tuple nobody asks for, but its stored state is held rather than
	// left unmeasured (see the held skip in Evaluate).
	Held bool
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
// tuple absent from it has never been evaluated, and one marked unmeasured
// since its last judgement (TupleState.UnmeasuredAt) is treated as such
// (backlog 4.42). Changes30 is how often each tuple changed inside the
// flapping window as the caller loaded it, without the transitions from
// before a tuple's latest unmeasured gap —
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
	// Ignored is which items an operator has decided not to judge this
	// tick, keyed by ItemKey rather than Tuple: a decision governs an
	// item, and every metric of that item shares its fate. It is resolved
	// by Load, not by Evaluate — the evaluator stays pure and keeps no
	// dependency on internal/items — so an absent key means watched, the
	// unoverruled default. Evaluate skips a tuple whose key is set here
	// exactly where it skips one with no resolved interval (see that skip),
	// and names it in Result.Dropped when a previous state exists to
	// delete.
	Ignored map[ItemKey]bool
	// Presence is the host's kind and its agent's presence (backlog 3.57;
	// see HostPresence). A client whose agent is not online is frozen —
	// Evaluate judges nothing and says so in Result.Frozen — and an online
	// client's staleness is measured from no earlier than the start of its
	// agent's online period. The zero value is a server without an agent, which
	// is judged exactly as before.
	Presence HostPresence
}

// Result is what one evaluation concluded: the new state of every tuple in
// the snapshot (in the snapshot's own order), the transitions to append to
// the history, the host roll-up, and when the host should be looked at
// again.
//
// Host is the zero HostState when the evaluation judged no tuple at all —
// either because the snapshot carried none, or because every one it
// carried was left out: for want of a resolved interval, or because an
// operator ignored it (see the two skips in Evaluate). A host with no
// tuples gets no roll-up row and renders as "no data" (the spec's step 7),
// so a zero Host is an instruction to the caller: write no row, and delete
// any check_host_states row the host still has. A roll-up is derived state
// — the history it was derived from lives in check_state_changes and is
// untouched — so leaving a stale one behind would hold the host at
// whatever colour it last was for ever, which is the very failure both
// exclusions exist to avoid.
// Host.HostID is left zero: the snapshot does not name the host, and the
// caller that loaded it already knows which one it is.
type Result struct {
	States  []TupleState
	Changes []Change
	Host    HostState
	NextDue time.Time
	// At is the evaluation's moment, Snapshot.Now: what Apply stamps the
	// rows of the Unmeasured tuples with (TupleState.UnmeasuredAt), on the
	// same clock as every state's EvaluatedAt it is compared against.
	At time.Time
	// Dropped lists the tuples whose stored check_states row must be
	// deleted because nothing judges them any more: an ignored tuple that
	// still has a Previous entry. Evaluate decides this; Apply deletes the
	// rows, inside the claim's transaction and before it writes the states
	// it did produce. This is the one place an ignored tuple is handled
	// differently from a tuple whose check has no resolved interval, which
	// deliberately leaves its row standing (see the skip in Evaluate): an
	// operator who ignores a volume sitting at crit must not be left with
	// a row that still says crit, with nothing left to re-judge it. A
	// dropped tuple writes no check_state_changes row either — that table
	// holds states, and "no longer judged" is not one; the audit log is
	// where the reason for the drop lives.
	Dropped []Tuple
	// Unmeasured lists the tuples this evaluation left out because their
	// check has no resolved interval any more, and which had a stored
	// state to leave standing. Unlike Dropped it is not an instruction to
	// delete: nothing deletes these rows (see the skip in Evaluate for
	// why). Apply only marks them (TupleState.UnmeasuredAt, backlog 4.42),
	// so that a tuple measured again starts fresh instead of resuming the
	// state it had before the gap. The report itself is for v0.4a's alert
	// sink: the sweep recognises a vanished subject by the absence of a
	// check_states row, and this is the one path that stops judging a
	// tuple while keeping one, so without this report an alert open on a
	// check an operator deleted or disabled could never close at all
	// (v0.4a final review, Critical). Reported every tick the tuple stays unmeasured, not only
	// the first: the consumer is idempotent (there is no alert left to
	// close on the second pass), and a once-only report would need state
	// this pure function deliberately does not keep.
	Unmeasured []Tuple
	// Frozen reports that the host is a client whose agent is not online
	// (backlog 3.57): nothing was judged. States, Changes, Dropped and
	// Unmeasured are empty, Host is the previous roll-up carried as it is
	// (zero when there is none), and the only thing the store writes is
	// the cursor's advance — Apply leaves every state row and the roll-up
	// exactly as they are. The alert sink must neither open, close nor
	// re-date anything on a tuple's behalf; Absent is the one host-level
	// decision it gets.
	Frozen bool
	// Absent reports, on a frozen result only, that the agent's last
	// contact (AgentLastSeen) is at least the organization's client absent
	// threshold ago — the input of the absent warning. Never set when the
	// warning is off.
	Absent bool
	// AgentLastSeen is the agent's last contact (HostPresence.AgentLastSeen)
	// on a frozen result, and zero otherwise.
	AgentLastSeen time.Time
}

// WindowRequests is what the store must fetch before Evaluate can run: one
// request per (item, window) that a winning rule's trigger or clear
// condition names, deduplicated, in the order they are first needed.
//
// It reads only Tuples, Effective, Ignored and Presence, so the ticker calls it on a
// Snapshot whose Windows field is still nil and fills that field from the
// result. Only rules judging a tuple Evaluate will actually judge
// contribute — one the host reports, whose check still has a resolved
// interval, and whose item is not ignored — since a window for anything
// else would be fetched and never read. The Ignored check mirrors Evaluate's
// own skip (see there): an operator who ignores an item specifically to
// stop paying for it must not keep paying for its window fetch forever on a
// verdict nobody reads.
func WindowRequests(s Snapshot) []WindowRequest {
	// A frozen client judges nothing (Evaluate's first branch), so none of
	// its windows would ever be read.
	if s.Presence.frozen() {
		return nil
	}
	reported := make(map[Tuple]bool, len(s.Tuples))
	for _, ts := range s.Tuples {
		if measured(ts) && !s.Ignored[itemKeyOf(ts.Tuple)] {
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
	// A client whose agent is off is expected to be silent: nothing about
	// it is judged until the agent is back (backlog 3.57, HostPresence).
	if s.Presence.frozen() {
		return evaluateFrozen(s)
	}
	inputs := buildInputs(s)
	byTuple := rulesPerTuple(s.Effective)

	var (
		states     []TupleState
		changes    []Change
		dropped    []Tuple
		unmeasured []Tuple
		// standing are the held tuples' stored states (the held skip
		// below): part of the roll-up, never written.
		standing []TupleState
		// pending collects every moment this host has a reason to be looked
		// at again: candidate ripenings and staleness deadlines (step 5).
		pending []time.Time
	)

	for _, ts := range s.Tuples {
		if ts.Held {
			// A tuple held on its item's state (TupleSnapshot.Held, backlog
			// 3.9b ruling): the agent reports nothing but the item's state
			// while the item is in it — a disk it leaves asleep, a pool
			// that is exported — so the tuple's last reading is not stale
			// and its stored state is still the last thing known about it.
			// It is handled like a frozen client, one tuple at a time: no
			// judgement, no write, not Unmeasured (its alert must neither
			// close nor move), and its stored state stands in the roll-up
			// as it is. When the item leaves the state, the tuple is
			// measured again and judged on that same stored state — no
			// fresh start, since nothing went unmeasured. Three things
			// still apply: an operator's ignore governs the item whatever
			// its state (Dropped, as below); a row marked unmeasured is no
			// state to hold (backlog 4.42); and a stored unknown is nothing
			// known — its reading went stale or failed before the item
			// entered the state, the staleness a held tuple is exempt from —
			// so it stands unwritten but keeps no host unknown.
			if prev, seen := s.Previous[ts.Tuple]; seen {
				switch {
				case s.Ignored[itemKeyOf(ts.Tuple)]:
					dropped = append(dropped, ts.Tuple)
				case !prev.unmeasuredSinceJudged() && prev.State != StateUnknown:
					standing = append(standing, prev)
				}
			}
			continue
		}
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
			//
			// Leaving the row standing is precisely why the tuple has to be
			// NAMED here rather than silently skipped (v0.4a final review,
			// Critical): v0.4a's alert sweep recognises a vanished subject
			// by the absence of its check_states row, and this is the one
			// path that takes a tuple out of the evaluation while keeping
			// that row — so an alert open on it would never close at all.
			// Unmeasured is the report that closes it, in the very
			// transaction this evaluation already holds. Only a tuple with
			// a stored state can hold an alert, which is why this mirrors
			// the Previous test the Ignored skip below already makes.
			//
			// The standing row is not a state to resume, though (backlog
			// 4.42): Apply marks it with this moment, and once the tuple is
			// measured again the marked row counts as no previous state at
			// all (returning, below).
			if _, seen := s.Previous[ts.Tuple]; seen {
				unmeasured = append(unmeasured, ts.Tuple)
			}
			continue
		}

		if s.Ignored[itemKeyOf(ts.Tuple)] {
			// An operator's decision governs an item, so every metric of it
			// shares the fate (Snapshot.Ignored's own doc comment): the
			// tuple is skipped exactly like one with no resolved interval —
			// no state, no change, no part in the roll-up, no pending
			// deadline. What differs is what happens to the row already on
			// disk. The no-resolved-interval skip above leaves it standing
			// on purpose: the check is gone, so nothing should touch data
			// nobody can refresh any more. An ignore is the opposite kind of
			// absence — a watched item an operator has deliberately silenced,
			// often while it is sitting at warn or crit at this very moment
			// — and leaving that row in place would show that colour for
			// ever with nothing left to re-judge it. So where a previous
			// state exists, its Tuple is reported in Dropped for Apply to
			// delete; a tuple ignored before its first evaluation has no row
			// to drop in the first place.
			if _, seen := s.Previous[ts.Tuple]; seen {
				dropped = append(dropped, ts.Tuple)
			}
			continue
		}

		prev, seen := s.Previous[ts.Tuple]
		// returning is a tuple back from an unmeasured gap (backlog 4.42,
		// owner decision: it starts fresh). Its row stood through the gap
		// only for the alert sink's sake (see the unmeasured skip above);
		// nothing in it describes the tuple now. Its since would date a
		// new alert before the previous episode closed, its candidate
		// would ripen on a wait nobody watched, its clear condition would
		// hold a severity judged before the gap, and its flapping would
		// count transitions from before it. So it is no previous state at
		// all: the tuple is judged as if for the first time.
		returning := seen && prev.unmeasuredSinceJudged()
		if returning {
			seen = false
		}
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
		if returning {
			// Load already leaves the transitions from before the gap out
			// of the window (CountStateChangesInFlappingWindow); a fresh
			// start has none of its own yet, whatever it is handed.
			count = 0
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
		if deadline := staleAt(ts, s.Presence); deadline.After(s.Now) {
			pending = append(pending, deadline)
		}
	}

	host := rollUp(append(slices.Clip(states), standing...), s.Now, s.PreviousHost)
	return Result{
		States:     states,
		Changes:    changes,
		Host:       host,
		At:         s.Now,
		NextDue:    graceDue(host, s.Presence, s.Now, nextDue(pending, states, len(changes) > 0, s.Now)),
		Dropped:    dropped,
		Unmeasured: unmeasured,
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
	if ts.Err != "" || s.Now.After(staleAt(ts, s.Presence)) {
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
	if isAlerting(prev.State) && StateRank(target) < StateRank(prev.State) {
		if holder, ok := bySeverity[severityOf(prev.State)]; ok && holder.Clear.Kind != "" && !Match(holder.Clear, in) {
			target, decider, decided = prev.State, holder, true
		}
	}

	// Step 4: for_duration, downward only. A deterioration has to hold
	// continuously for the rule's duration before it becomes the state; an
	// improvement takes effect at once, because a host that is healthy
	// again must not stay red for another ten minutes.
	if StateRank(target) <= StateRank(prev.State) {
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
// unknown; a disabled check never runs that path at all. Since backlog
// 4.42 such a row is marked unmeasured and starts fresh before this is
// ever asked (returning, in Evaluate); the guard still covers a row that
// went unmeasured before the mark existed, which carries none.
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
//
// For an online client the clock starts no earlier than the start of its
// agent's online period (HostPresence.stalenessAnchor, backlog 3.57): a
// check that has not run since the notebook woke up is not unknown the
// moment the agent reconnects. Every other host has no anchor, and the
// reading alone decides — today's rule exactly.
func staleAt(ts TupleSnapshot, p HostPresence) time.Time {
	from := ts.At
	if anchor := p.stalenessAnchor(); anchor.After(from) {
		from = anchor
	}
	return from.Add(stalenessFactor * ts.Interval)
}

// nextDue is step 5: the earliest moment this host has a reason to be
// looked at again, or a cheap heartbeat when it has none. That fallback is
// what makes unknown reachable at all — a host whose agent has died sends
// nothing, so nothing marks it dirty and only a due time brings the ticker
// back to it.
//
// The fallback is the long one once the host has arrived where that
// journey ends: every tuple unknown, and this pass changed nothing. See
// silentHeartbeat for why that is free. Anything else — a single tuple
// still judged, or any change at all — keeps the short one, including a
// host part-way into going silent, which is still a host producing news.
func nextDue(pending []time.Time, states []TupleState, changed bool, now time.Time) time.Time {
	if len(pending) == 0 {
		if silent(states, changed) {
			return now.Add(silentHeartbeat)
		}
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

// silent reports whether this pass produced nothing but a repeat of what
// was already stored: at least one tuple, every one of them unknown, and
// no change. A host with NO states is deliberately not silent — it writes
// no row at all (see rollUp), so there is nothing to back off from, and a
// host whose checks are being set up should not wait five minutes for its
// first verdict.
func silent(states []TupleState, changed bool) bool {
	if changed || len(states) == 0 {
		return false
	}
	for _, st := range states {
		if st.State != StateUnknown {
			return false
		}
	}
	return true
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
