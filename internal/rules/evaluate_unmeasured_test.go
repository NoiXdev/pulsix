package rules_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// Backlog 4.42: a tuple that went unmeasured (its check disabled or taken
// off the host, the host un-monitored, a selector edited, a metric no
// longer asked for) keeps its check_states row, marked with the moment it
// went unmeasured (TupleState.UnmeasuredAt). When it is measured again it
// starts fresh: the marked row is no previous state at all, exactly as if
// the tuple were judged for the first time.

// wasUnmeasured is a previous state last judged at evaluatedAt and marked
// unmeasured at unmeasuredAt.
func wasUnmeasured(s rules.TupleState, evaluatedAt, unmeasuredAt time.Time) rules.TupleState {
	s.EvaluatedAt = evaluatedAt
	s.UnmeasuredAt = &unmeasuredAt
	return s
}

func TestEvaluateUnmeasured(t *testing.T) {
	flapping := wasBy(cpuT, rules.StateCrit, at(0), critID)
	flapping.Flapping = true

	for _, tc := range []evalCase{
		{
			// The defect itself: without the mark the old since (minute 0)
			// is carried, and an alert opened from it would start before
			// the previous episode closed. Fresh, it is the first new
			// judgement; no change is written (an insert, not a
			// transition), and no flapping is carried: neither the flag
			// nor the transitions counted before the gap.
			name: "a crit row back from unmeasured starts a fresh crit",
			snap: rules.Snapshot{
				Now:       at(30),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(30), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(wasUnmeasured(flapping, at(5), at(6))),
				// The store leaves pre-gap changes out of the count (Load);
				// even handed them, the first judgement counts none.
				Changes30: map[rules.Tuple]int{cpuT: 5},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(30),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(30),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(30), EvaluatedAt: at(30)},
				NextDue: at(60),
			},
		},
		{
			// A for_duration wait counts anew, even across a gap short
			// enough that `continuous` alone would have carried the
			// candidate (and promoted it at once, its wait long over).
			name: "a candidate does not survive an unmeasured gap inside the staleness window",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous: prev(wasUnmeasured(
					wasRipening(cpuT, rules.StateOK, at(0), rules.StateCrit, at(5)), at(10), at(11))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(20),
					LastValue: ptr(95.0), Candidate: rules.StateCrit, CandidateSince: ptr(at(20)),
					EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(30),
			},
		},
		{
			// Hysteresis holds a state the tuple was judged into; a row
			// back from unmeasured holds nothing, so a value between the
			// clear and the trigger threshold reads ok.
			name: "a clear condition does not hold a crit across an unmeasured gap",
			snap: rules.Snapshot{
				Now:    at(30),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 85, at(30), tenMin)},
				Effective: []rules.Effective{eff(
					withClear(cpuCrit(), compare("<", metric("cpu.used_pct"), number(80))), cpuT)},
				Previous: prev(wasUnmeasured(wasBy(cpuT, rules.StateCrit, at(0), critID), at(5), at(6))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(30),
					LastValue: ptr(85.0), EvaluatedAt: at(30),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(30), EvaluatedAt: at(30)},
				NextDue: at(60),
			},
		},
		{
			// A mark the tuple has been judged since is history: the row
			// is an ordinary previous state again and its since carries.
			name: "a mark older than the last judgement is no gap",
			snap: rules.Snapshot{
				Now:       at(30),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(30), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(wasUnmeasured(wasBy(cpuT, rules.StateCrit, at(0), critID), at(29), at(6))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(0),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(30),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(0), EvaluatedAt: at(30)},
				NextDue: at(60),
			},
		},
		{
			// While the tuple stays unmeasured it is reported every tick,
			// marked or not: the alert sink's report is unchanged.
			name: "a marked row still unmeasured is still reported",
			snap: rules.Snapshot{
				Now:       at(30),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(30), 0)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(wasUnmeasured(wasBy(cpuT, rules.StateCrit, at(0), critID), at(5), at(6))),
			},
			want: rules.Result{
				Unmeasured: []rules.Tuple{cpuT},
				NextDue:    at(30).Add(15 * time.Second),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertResult(t, rules.Evaluate(tc.snap), tc.want)
		})
	}
}
