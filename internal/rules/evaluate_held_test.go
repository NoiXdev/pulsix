package rules_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// Backlog 3.9b ruling: a tuple its descriptor holds on its item's current
// state (TupleSnapshot.Held: a sleeping disk's sector count, an exported
// pool's capacity) is neither judged nor unmeasured. Its stored state
// stands exactly as it is — not rewritten, not marked — and counts
// towards the roll-up as it stood, so its alert neither closes nor moves;
// once the item leaves that state, judging resumes on the stored state.

var (
	sectorsT = rules.Tuple{CheckKey: "smart", Item: "/dev/sdd", Metric: "smart.reallocated_sectors"}
	modeT    = rules.Tuple{CheckKey: "smart", Item: "/dev/sdd", Metric: "smart.power_mode"}
)

// held is the snapshot of a tuple waiting on its item's state: its last
// reading, long ago, and no interval to judge it against.
func held(t rules.Tuple, value float64, measured time.Time) rules.TupleSnapshot {
	return rules.TupleSnapshot{Tuple: t, Value: ptr(value), At: measured, Held: true}
}

func labelSnap(t rules.Tuple, l string, measured time.Time, interval time.Duration) rules.TupleSnapshot {
	return rules.TupleSnapshot{Tuple: t, Label: ptr(l), At: measured, Interval: interval}
}

func TestEvaluateHeld(t *testing.T) {
	sectorsCrit := evalRule(critID, sectorsT, rules.SeverityCrit, compare(">", metric("smart.reallocated_sectors"), number(0)))
	crit := wasBy(sectorsT, rules.StateCrit, at(0), critID)
	crit.EvaluatedAt = at(5)
	modeOK := was(modeT, rules.StateOK, at(0))
	modeOK.EvaluatedAt = at(5)

	for _, tc := range []evalCase{
		{
			// The defect: unmeasured, the crit row would be reported and
			// its alert closed stale, the roll-up would read ok. Held, the
			// row is not written, the alert sink hears nothing about it,
			// and the host stays crit on the stored state.
			name: "a held crit keeps the host crit and is neither judged nor unmeasured",
			snap: rules.Snapshot{
				Now: at(60),
				Tuples: []rules.TupleSnapshot{
					labelSnap(modeT, "standby", at(60), tenMin),
					held(sectorsT, 3, at(1)),
				},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(modeOK, crit),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: modeT, State: rules.StateOK, Since: at(0), LastLabel: ptr("standby"), EvaluatedAt: at(60),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(0), EvaluatedAt: at(60)},
				NextDue: at(90),
			},
		},
		{
			// Nothing but held tuples still rolls up: a zero Host would
			// delete the roll-up and close the host's alerts as stale.
			name: "a host with only held tuples rolls up their stored states",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(crit),
			},
			want: rules.Result{
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(0), EvaluatedAt: at(60)},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			// The roll-up carries its own since when the held state keeps
			// it where it was.
			name: "a held crit keeps the previous roll-up's since",
			snap: rules.Snapshot{
				Now:          at(60),
				Tuples:       []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective:    []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:     prev(crit),
				PreviousHost: &rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(2), EvaluatedAt: at(5)},
			},
			want: rules.Result{
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(2), EvaluatedAt: at(60)},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			name: "a held tuple never judged has nothing to hold",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
			},
			want: rules.Result{NextDue: at(60).Add(15 * time.Second)},
		},
		{
			// A row marked unmeasured is no state to resume (backlog 4.42),
			// so it is no state to hold either.
			name: "a held tuple whose row was marked unmeasured holds nothing",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(wasUnmeasured(crit, at(5), at(6))),
			},
			want: rules.Result{NextDue: at(60).Add(15 * time.Second)},
		},
		{
			// A held state already unknown says nothing known: the reading
			// went stale, or failed, before the item entered its state (an
			// agent updated while the disk slept). It stands, unwritten, but
			// does not hold the host unknown — staleness is exactly what a
			// held tuple is exempt from — and the first reading after the
			// item leaves the state judges it.
			name: "a held unknown stands but does not hold the host unknown",
			snap: rules.Snapshot{
				Now: at(60),
				Tuples: []rules.TupleSnapshot{
					labelSnap(modeT, "standby", at(60), tenMin),
					held(sectorsT, 3, at(1)),
				},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(modeOK, was(sectorsT, rules.StateUnknown, at(40))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: modeT, State: rules.StateOK, Since: at(0), LastLabel: ptr("standby"), EvaluatedAt: at(60),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(60)},
				NextDue: at(90),
			},
		},
		{
			// An operator's ignore governs the item whatever its state.
			name: "a held tuple of an ignored item is dropped",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(crit),
				Ignored:   map[rules.ItemKey]bool{{CheckKey: "smart", Item: "/dev/sdd"}: true},
			},
			want: rules.Result{
				Dropped: []rules.Tuple{sectorsT},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			// Awake again: the first new reading is judged on the stored
			// state — no fresh start, so the crit keeps its since and no
			// transition is written.
			name: "a tuple no longer held resumes its stored state",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{snap(sectorsT, 3, at(60), tenMin)},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(crit),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: sectorsT, State: rules.StateCrit, Since: at(0),
					RuleID: &critID, LastValue: ptr(3.0), EvaluatedAt: at(60),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(0), EvaluatedAt: at(60)},
				NextDue: at(90),
			},
		},
		{
			// The operator deleted or disabled the rule that put the held
			// tuple at crit (its row's rule_id is set to NULL, or names a
			// rule no longer in force): nothing holds it there any more, so
			// it resolves as an awake tuple would — judged ok on no rule,
			// a transition written, the alert closing resolved — and the
			// roll-up drops the crit. From then on it holds that ok.
			name: "a held crit whose rule is gone resolves to ok",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: nil,
				Previous:  prev(crit),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: sectorsT, State: rules.StateOK, Since: at(60), LastValue: ptr(3.0), EvaluatedAt: at(60),
				}},
				Changes: []rules.Change{{At: at(60), Tuple: sectorsT, From: rules.StateCrit, To: rules.StateOK, Value: ptr(3.0)}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(60), EvaluatedAt: at(60)},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			name: "a held crit whose rule_id was cleared resolves to ok",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: []rules.Effective{eff(sectorsCrit, sectorsT)},
				Previous:  prev(was(sectorsT, rules.StateCrit, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: sectorsT, State: rules.StateOK, Since: at(60), LastValue: ptr(3.0), EvaluatedAt: at(60),
				}},
				Changes: []rules.Change{{At: at(60), Tuple: sectorsT, From: rules.StateCrit, To: rules.StateOK, Value: ptr(3.0)}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(60), EvaluatedAt: at(60)},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			// Another rule now wins for the tuple: the one that judged it no
			// longer applies, and the new one judges the next reading.
			name: "a held crit whose rule was replaced resolves to ok",
			snap: rules.Snapshot{
				Now:       at(60),
				Tuples:    []rules.TupleSnapshot{held(sectorsT, 3, at(1))},
				Effective: []rules.Effective{eff(evalRule(warnID, sectorsT, rules.SeverityCrit, compare(">", metric("smart.reallocated_sectors"), number(10))), sectorsT)},
				Previous:  prev(crit),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: sectorsT, State: rules.StateOK, Since: at(60), LastValue: ptr(3.0), EvaluatedAt: at(60),
				}},
				Changes: []rules.Change{{At: at(60), Tuple: sectorsT, From: rules.StateCrit, To: rules.StateOK, Value: ptr(3.0)}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(60), EvaluatedAt: at(60)},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			// A held ok needs no rule to stand on.
			name: "a held ok stands without a rule",
			snap: rules.Snapshot{
				Now:      at(60),
				Tuples:   []rules.TupleSnapshot{held(sectorsT, 0, at(1))},
				Previous: prev(was(sectorsT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(60)},
				NextDue: at(60).Add(15 * time.Second),
			},
		},
		{
			// Back from a hold (TupleSnapshot.Resumed), the first new reading
			// decides: a for_duration wait that was ripening when the item
			// entered its state starts over, even when the hold was shorter
			// than the staleness window that would otherwise carry it.
			name: "a candidate does not survive a hold",
			snap: rules.Snapshot{
				Now: at(60),
				Tuples: []rules.TupleSnapshot{func() rules.TupleSnapshot {
					ts := snap(sectorsT, 3, at(60), tenMin)
					ts.Resumed = true
					return ts
				}()},
				Effective: []rules.Effective{eff(withFor(sectorsCrit, tenMin), sectorsT)},
				Previous:  prev(wasRipeningLastSeen(sectorsT, rules.StateOK, at(0), rules.StateCrit, at(50), at(55))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: sectorsT, State: rules.StateOK, Since: at(0), LastValue: ptr(3.0),
					Candidate: rules.StateCrit, CandidateSince: ptr(at(60)), EvaluatedAt: at(60),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(60)},
				NextDue: at(70),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertResult(t, rules.Evaluate(tc.snap), tc.want)
		})
	}
}
