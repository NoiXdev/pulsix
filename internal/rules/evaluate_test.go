package rules_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// The evaluation table below is one case per row: a fully specified
// Snapshot and the whole Result it must produce — states, changes, host
// roll-up and NextDue, never only the field the case is named after. The
// file is long on purpose. Every interesting behaviour of the state
// machine lives in the interaction between hysteresis, for_duration and
// unknown, and the order those three are applied in is the design; a
// thinner table would stop pinning that order down.

// evalEpoch anchors every timestamp in this file, so a case reads as
// "minute 10" rather than as a date, and every expectation is a value
// derived from the same constant rather than from the system clock.
var evalEpoch = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// at is minute m after the epoch.
func at(m int) time.Time { return evalEpoch.Add(time.Duration(m) * time.Minute) }

// The tuples the table judges. cpuLoadT shares cpuT's ItemKey, and
// diskFreeRootT shares diskRootT's, which is what the sibling-metric and
// per-item window cases rely on.
var (
	cpuT          = rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	cpuLoadT      = rules.Tuple{CheckKey: "cpu", Metric: "cpu.load_avg1"}
	memT          = rules.Tuple{CheckKey: "memory", Metric: "memory.used_pct"}
	diskRootT     = rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	diskVarT      = rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.used_pct"}
	diskFreeRootT = rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.free_bytes"}
	diskFreeVarT  = rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.free_bytes"}
	diskTmpT      = rules.Tuple{CheckKey: "disk", Item: "/tmp", Metric: "disk.used_pct"}
	svcT          = rules.Tuple{CheckKey: "service", Item: "nginx", Metric: "service.state"}
)

// Fixed rule ids, so an expected TupleState can name the rule that decided
// it without the table having to thread a generated uuid through.
var (
	critID    = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	warnID    = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	memCritID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
)

const (
	tenMin = 10 * time.Minute
	oneMin = time.Minute
)

// evalRule builds an enabled org-scoped rule for one tuple's metric.
func evalRule(id uuid.UUID, t rules.Tuple, sev rules.Severity, trigger rules.Node) rules.Rule {
	return rules.Rule{
		ID:       id,
		Enabled:  true,
		Scope:    rules.ScopeOrg,
		CheckKey: t.CheckKey,
		Metric:   t.Metric,
		Severity: sev,
		Mode:     "expression",
		Trigger:  trigger,
	}
}

func withFor(r rules.Rule, d time.Duration) rules.Rule { r.For = d; return r }
func withClear(r rules.Rule, n rules.Node) rules.Rule  { r.Clear = n; return r }
func eff(r rules.Rule, t rules.Tuple) rules.Effective {
	return rules.Effective{Tuple: t, Severity: r.Severity, Winner: r, Observed: true}
}

// cpuCrit and cpuWarn are the two rules most of the table judges with:
// "cpu.used_pct > 90" at crit and "> 80" at warn.
func cpuCrit() rules.Rule {
	return evalRule(critID, cpuT, rules.SeverityCrit, compare(">", metric("cpu.used_pct"), number(90)))
}

func cpuWarn() rules.Rule {
	return evalRule(warnID, cpuT, rules.SeverityWarn, compare(">", metric("cpu.used_pct"), number(80)))
}

// snap is a healthy measurement: one numeric value, measured at `measured`,
// belonging to a check whose resolved interval is `interval`.
func snap(t rules.Tuple, value float64, measured time.Time, interval time.Duration) rules.TupleSnapshot {
	return rules.TupleSnapshot{Tuple: t, Value: ptr(value), At: measured, Interval: interval}
}

// was is a previous state with nothing half-ripened and no rule recorded.
func was(t rules.Tuple, st rules.State, since time.Time) rules.TupleState {
	return rules.TupleState{Tuple: t, State: st, Since: since}
}

// wasBy is a previous state a rule decided.
func wasBy(t rules.Tuple, st rules.State, since time.Time, ruleID uuid.UUID) rules.TupleState {
	s := was(t, st, since)
	s.RuleID = &ruleID
	return s
}

// wasRipening is a previous state with a candidate waiting to ripen.
func wasRipening(t rules.Tuple, st rules.State, since time.Time, cand rules.State, candSince time.Time) rules.TupleState {
	s := was(t, st, since)
	s.Candidate = cand
	s.CandidateSince = &candSince
	return s
}

func prev(states ...rules.TupleState) map[rules.Tuple]rules.TupleState {
	m := make(map[rules.Tuple]rules.TupleState, len(states))
	for _, s := range states {
		m[s.Tuple] = s
	}
	return m
}

type evalCase struct {
	name string
	snap rules.Snapshot
	want rules.Result
}

func TestEvaluate(t *testing.T) {
	for _, tc := range evaluateCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertResult(t, rules.Evaluate(tc.snap), tc.want)
		})
	}
}

func evaluateCases() []evalCase {
	return []evalCase{
		{
			// Step 2 and step 4 with nothing to wait for: the threshold is
			// crossed and For is zero, so the state moves on this tick.
			name: "a crossed threshold with no duration goes crit at once",
			snap: rules.Snapshot{
				Now:       at(10),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(10), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(10),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(10),
				}},
				Changes: []rules.Change{{
					At: at(10), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(10), EvaluatedAt: at(10)},
				NextDue: at(40),
			},
		},
		{
			// Step 4 downward: the deterioration is only a candidate, and
			// step 5 brings the ticker back exactly when it ripens.
			name: "a crossed threshold with a duration only sets a candidate",
			snap: rules.Snapshot{
				Now:       at(10),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(10), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(95.0), Candidate: rules.StateCrit, CandidateSince: ptr(at(10)),
					EvaluatedAt: at(10),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(10)},
				NextDue: at(20),
			},
		},
		{
			// Exactly at the ripening moment, not a tick later: the
			// comparison is "not before", so the boundary ripens.
			name: "a candidate ripens exactly at its ripening moment",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(wasRipening(cpuT, rules.StateOK, at(0), rules.StateCrit, at(10))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// The history records states, not intentions: a candidate that
			// flips away before it ripens leaves no row behind at all.
			name: "a candidate that recovers before ripening writes no change",
			snap: rules.Snapshot{
				Now:       at(15),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(15), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(wasRipening(cpuT, rules.StateOK, at(0), rules.StateCrit, at(10))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), EvaluatedAt: at(15),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(15)},
				NextDue: at(45),
			},
		},
		{
			// for_duration is downward only: a host that is healthy again
			// must not stay red for another ten minutes.
			name: "a recovery from crit takes effect at once despite a duration",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(20),
					LastValue: ptr(50.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateCrit, To: rules.StateOK,
					Value: ptr(50.0),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Step 3: the trigger is already false, but the clear predicate
			// is not yet true, so the crit holds and Since does not move.
			name: "hysteresis holds a crit while the clear predicate is false",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 80, at(20), tenMin)},
				Effective: []rules.Effective{eff(withClear(cpuCrit(), compare("<", metric("cpu.used_pct"), number(70))), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(5),
					RuleID: &critID, LastValue: ptr(80.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(5), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// The same hold one severity down: hysteresis is a property of
			// the rule that decided the state, whichever severity that was.
			name: "hysteresis holds a warn while the clear predicate is false",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 75, at(20), tenMin)},
				Effective: []rules.Effective{eff(withClear(cpuWarn(), compare("<", metric("cpu.used_pct"), number(70))), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateWarn, at(5), warnID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateWarn, Since: at(5),
					RuleID: &warnID, LastValue: ptr(75.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateWarn, Warn: 1, Since: at(5), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			name: "hysteresis releases the crit when the clear predicate turns true",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 60, at(20), tenMin)},
				Effective: []rules.Effective{eff(withClear(cpuCrit(), compare("<", metric("cpu.used_pct"), number(70))), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(20),
					LastValue: ptr(60.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateCrit, To: rules.StateOK,
					Value: ptr(60.0),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// A rule with no clear condition clears as soon as the trigger
			// is false — hysteresis is opt-in, not the default.
			name: "a rule without a clear condition clears as soon as the trigger is false",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 80, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(20),
					LastValue: ptr(80.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateCrit, To: rules.StateOK,
					Value: ptr(80.0),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Step 1 by staleness: the value would be a crit, but it is
			// older than three intervals, so no rule gets to speak. The
			// staleness deadline is already behind us, so step 5 falls back
			// to the cheap heartbeat rather than scheduling into the past.
			name: "a value older than three intervals goes unknown",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(10), oneMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateUnknown, Since: at(20),
					LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown,
					Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(20).Add(15 * time.Second),
			},
		},
		{
			// Step 1 by collection error: the value is fresh and would
			// cross the threshold, but the agent could not collect it.
			name: "a collection error goes unknown even with a fresh value",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{{
					Tuple: cpuT, Value: ptr(95.0), Err: "read /proc/stat: permission denied",
					At: at(20), Interval: tenMin,
				}},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateUnknown, Since: at(20),
					LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown,
					Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Review focus 3: the agent goes silent while a candidate is
			// ripening. unknown outranks the candidate and the candidate is
			// discarded rather than ripening in the dark.
			name: "an agent going silent mid-candidate discards the candidate",
			snap: rules.Snapshot{
				Now:       at(30),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(10), 5*time.Minute)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(wasRipening(cpuT, rules.StateOK, at(0), rules.StateCrit, at(10))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateUnknown, Since: at(30),
					LastValue: ptr(95.0), EvaluatedAt: at(30),
				}},
				Changes: []rules.Change{{
					At: at(30), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown,
					Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(30), EvaluatedAt: at(30)},
				NextDue: at(30).Add(15 * time.Second),
			},
		},
		{
			// unknown is decided before hysteresis, so a clear condition
			// that is still false cannot hold a crit against missing data.
			name: "unknown outranks a crit that hysteresis would otherwise hold",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(5), oneMin)},
				Effective: []rules.Effective{eff(withClear(cpuCrit(), compare("<", metric("cpu.used_pct"), number(70))), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateUnknown, Since: at(20),
					LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateCrit, To: rules.StateUnknown,
					Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(20).Add(15 * time.Second),
			},
		},
		{
			// Review focus 4: candidate_since is a row, so a for_duration
			// window survives a manager restart. The wait started at
			// minute 5 and the first tick after the restart is at minute
			// 30 — the state must not restart its clock.
			name: "a candidate from before a restart ripens on the first tick after it",
			snap: rules.Snapshot{
				Now:       at(30),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(30), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(wasRipening(cpuT, rules.StateOK, at(0), rules.StateCrit, at(5))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(30),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(30),
				}},
				Changes: []rules.Change{{
					At: at(30), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(30), EvaluatedAt: at(30)},
				NextDue: at(60),
			},
		},
		{
			name: "flapping is set at five changes",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
				Changes30: map[rules.Tuple]int{cpuT: 5},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), Flapping: true, EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// The flag has its own hysteresis: four is neither set nor
			// clear, so a set flag stays set.
			name: "flapping is held at four changes",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous: prev(rules.TupleState{
					Tuple: cpuT, State: rules.StateOK, Since: at(0), Flapping: true,
				}),
				Changes30: map[rules.Tuple]int{cpuT: 4},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), Flapping: true, EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// … and a clear flag stays clear at four: the carry-over goes
			// both ways, which is what makes the band a hysteresis rather
			// than a second threshold.
			name: "flapping stays clear at four changes",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
				Changes30: map[rules.Tuple]int{cpuT: 4},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Three is the lower edge of the carry-over band: still not
			// enough to clear the flag, which is what stops the flag itself
			// from flapping between four and three.
			name: "flapping is held at three changes",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous: prev(rules.TupleState{
					Tuple: cpuT, State: rules.StateOK, Since: at(0), Flapping: true,
				}),
				Changes30: map[rules.Tuple]int{cpuT: 3},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), Flapping: true, EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			name: "flapping is cleared at two changes",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous: prev(rules.TupleState{
					Tuple: cpuT, State: rules.StateOK, Since: at(0), Flapping: true,
				}),
				Changes30: map[rules.Tuple]int{cpuT: 2},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Step 7 with a mixed host: crit wins the roll-up, and the
			// unknown is counted beside it rather than masked by it.
			name: "the roll-up over one crit, one unknown and three ok is crit",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(cpuT, 95, at(20), tenMin),
					snap(memT, 40, at(5), oneMin), // stale
					snap(diskRootT, 10, at(20), tenMin),
					snap(diskVarT, 12, at(20), tenMin),
					snap(cpuLoadT, 0.5, at(20), tenMin),
				},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous: prev(
					was(cpuT, rules.StateOK, at(0)),
					was(memT, rules.StateOK, at(0)),
					was(diskRootT, rules.StateOK, at(0)),
					was(diskVarT, rules.StateOK, at(0)),
					was(cpuLoadT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: cpuT, State: rules.StateCrit, Since: at(20), RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: memT, State: rules.StateUnknown, Since: at(20), LastValue: ptr(40.0), EvaluatedAt: at(20)},
					{Tuple: diskRootT, State: rules.StateOK, Since: at(0), LastValue: ptr(10.0), EvaluatedAt: at(20)},
					{Tuple: diskVarT, State: rules.StateOK, Since: at(0), LastValue: ptr(12.0), EvaluatedAt: at(20)},
					{Tuple: cpuLoadT, State: rules.StateOK, Since: at(0), LastValue: ptr(0.5), EvaluatedAt: at(20)},
				},
				Changes: []rules.Change{
					{At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit, RuleID: &critID, Value: ptr(95.0)},
					{At: at(20), Tuple: memT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(40.0)},
				},
				Host: rules.HostState{
					State: rules.StateCrit, Crit: 1, Warn: 0, Unknown: 1,
					Since: at(20), EvaluatedAt: at(20),
				},
				NextDue: at(50),
			},
		},
		{
			name: "a host whose only tuples are unknown rolls up to unknown",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(cpuT, 95, at(5), oneMin),
					snap(memT, 40, at(5), oneMin),
				},
				Previous: prev(
					was(cpuT, rules.StateOK, at(0)),
					was(memT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: cpuT, State: rules.StateUnknown, Since: at(20), LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: memT, State: rules.StateUnknown, Since: at(20), LastValue: ptr(40.0), EvaluatedAt: at(20)},
				},
				Changes: []rules.Change{
					{At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(95.0)},
					{At: at(20), Tuple: memT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(40.0)},
				},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 2, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(20).Add(15 * time.Second),
			},
		},
		{
			// A host with no tuples at all gets no roll-up row: the zero
			// HostState is how Evaluate says "write nothing".
			name: "a host with no tuples writes no host state",
			snap: rules.Snapshot{Now: at(20)},
			want: rules.Result{NextDue: at(20).Add(15 * time.Second)},
		},
		{
			// Step 2 is severity-ordered, not slice-ordered: the warn rule
			// comes first in Effective and still loses.
			name: "crit outranks a warn that also matches",
			snap: rules.Snapshot{
				Now:    at(20),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{
					eff(cpuWarn(), cpuT),
					eff(cpuCrit(), cpuT),
				},
				Previous: prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			name: "only the warn threshold crossed is a warn",
			snap: rules.Snapshot{
				Now:    at(20),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 85, at(20), tenMin)},
				Effective: []rules.Effective{
					eff(cpuWarn(), cpuT),
					eff(cpuCrit(), cpuT),
				},
				Previous: prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateWarn, Since: at(20),
					RuleID: &warnID, LastValue: ptr(85.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateWarn,
					RuleID: &warnID, Value: ptr(85.0),
				}},
				Host:    rules.HostState{State: rules.StateWarn, Warn: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// A tuple's first ever evaluation is an insert, not a
			// transition: there is no earlier state to move away from, so
			// no Change is written.
			name: "a tuple seen for the first time writes a state but no change",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// A first evaluation is judged against ok, so a for_duration
			// rule still has to ripen: nothing yet says the condition held
			// before this very tick.
			name: "a tuple seen for the first time still has to ripen its duration",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
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
			// The binding invariant: a rule that cannot be evaluated never
			// escalates. The window aggregate is missing, so the rule does
			// not match and the tuple stays ok.
			name: "a rule whose window has no value does not escalate",
			snap: rules.Snapshot{
				Now:    at(20),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(
					evalRule(critID, cpuT, rules.SeverityCrit,
						compare(">", window("cpu.used_pct", "avg", 300), number(90))),
					cpuT,
				)},
				Previous: prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Window values are item-relative: the same rule reads "/"'s
			// own average for "/" and "/var"'s for "/var".
			name: "a window is read for the tuple's own item",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(diskRootT, 95, at(20), tenMin),
					snap(diskVarT, 10, at(20), tenMin),
				},
				Effective: []rules.Effective{
					eff(diskWindowRule(), diskRootT),
					eff(diskWindowRule(), diskVarT),
				},
				Windows: map[rules.WindowRequest]float64{
					{Key: rules.ItemKey{CheckKey: "disk", Item: "/"}, Metric: "disk.used_pct", Agg: "avg", Seconds: 300}:    95,
					{Key: rules.ItemKey{CheckKey: "disk", Item: "/var"}, Metric: "disk.used_pct", Agg: "avg", Seconds: 300}: 10,
				},
				Previous: prev(
					was(diskRootT, rules.StateOK, at(0)),
					was(diskVarT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: diskRootT, State: rules.StateCrit, Since: at(20), RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: diskVarT, State: rules.StateOK, Since: at(0), LastValue: ptr(10.0), EvaluatedAt: at(20)},
				},
				Changes: []rules.Change{
					{At: at(20), Tuple: diskRootT, From: rules.StateOK, To: rules.StateCrit, RuleID: &critID, Value: ptr(95.0)},
				},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// One Input per ItemKey, not per tuple: the rule on
			// disk.used_pct reads disk.free_bytes of the same mount, and
			// only of the same mount.
			name: "a rule reads a sibling metric of the same item",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(diskRootT, 95, at(20), tenMin),
					snap(diskFreeRootT, 100, at(20), tenMin),
					snap(diskVarT, 95, at(20), tenMin),
					snap(diskFreeVarT, 5e9, at(20), tenMin),
				},
				Effective: []rules.Effective{
					eff(diskSiblingRule(), diskRootT),
					eff(diskSiblingRule(), diskVarT),
				},
				Previous: prev(
					was(diskRootT, rules.StateOK, at(0)),
					was(diskFreeRootT, rules.StateOK, at(0)),
					was(diskVarT, rules.StateOK, at(0)),
					was(diskFreeVarT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: diskRootT, State: rules.StateCrit, Since: at(20), RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: diskFreeRootT, State: rules.StateOK, Since: at(0), LastValue: ptr(100.0), EvaluatedAt: at(20)},
					{Tuple: diskVarT, State: rules.StateOK, Since: at(0), LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: diskFreeVarT, State: rules.StateOK, Since: at(0), LastValue: ptr(5e9), EvaluatedAt: at(20)},
				},
				Changes: []rules.Change{
					{At: at(20), Tuple: diskRootT, From: rules.StateOK, To: rules.StateCrit, RuleID: &critID, Value: ptr(95.0)},
				},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// An escalation is a deterioration like any other, so the crit
			// rule's own duration applies while the warn holds.
			name: "an escalation from warn to crit waits for the crit rule's duration",
			snap: rules.Snapshot{
				Now:    at(20),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{
					eff(cpuWarn(), cpuT),
					eff(withFor(cpuCrit(), tenMin), cpuT),
				},
				Previous: prev(wasBy(cpuT, rules.StateWarn, at(5), warnID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateWarn, Since: at(5),
					RuleID: &warnID, LastValue: ptr(95.0),
					Candidate: rules.StateCrit, CandidateSince: ptr(at(20)),
					EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateWarn, Warn: 1, Since: at(5), EvaluatedAt: at(20)},
				NextDue: at(30),
			},
		},
		{
			// Hysteresis only ever holds a state against an improvement; it
			// must never stand in the way of an escalation.
			name: "hysteresis does not block an escalation",
			snap: rules.Snapshot{
				Now:    at(20),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{
					eff(withClear(cpuWarn(), compare("<", metric("cpu.used_pct"), number(70))), cpuT),
					eff(cpuCrit(), cpuT),
				},
				Previous: prev(wasBy(cpuT, rules.StateWarn, at(5), warnID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateWarn, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// The candidate is keyed on the target state: a deterioration
			// that changes shape is a different wait, so the clock restarts.
			name: "a candidate whose target changes restarts its clock",
			snap: rules.Snapshot{
				Now:    at(20),
				Tuples: []rules.TupleSnapshot{snap(cpuT, 85, at(20), tenMin)},
				Effective: []rules.Effective{
					eff(withFor(cpuWarn(), tenMin), cpuT),
					eff(withFor(cpuCrit(), tenMin), cpuT),
				},
				Previous: prev(wasRipening(cpuT, rules.StateOK, at(0), rules.StateCrit, at(10))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(85.0), Candidate: rules.StateWarn, CandidateSince: ptr(at(20)),
					EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(30),
			},
		},
		{
			name: "a discrete metric is judged by its label",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{{
					Tuple: svcT, Label: ptr("failed"), At: at(20), Interval: tenMin,
				}},
				Effective: []rules.Effective{eff(
					evalRule(critID, svcT, rules.SeverityCrit, label("service.state", "!=", "running")),
					svcT,
				)},
				Previous: prev(was(svcT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: svcT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastLabel: ptr("failed"), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: svcT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Label: ptr("failed"),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// A check with no resolved interval is one nothing asks anyone
			// to measure any more. Its tuple is left out entirely: no
			// state, no change, no part in the roll-up, and — because it is
			// out of Tuples as far as this evaluation is concerned — no
			// value its siblings could read either. The value on hand would
			// cross the crit threshold, and the stored state row still says
			// crit; neither gets a say, and neither is rewritten.
			name: "a tuple whose check has no resolved interval is left out entirely",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(cpuT, 95, at(0), 0),
					snap(memT, 50, at(20), tenMin),
				},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous: prev(
					wasBy(cpuT, rules.StateCrit, at(5), critID),
					was(memT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: memT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(50.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// The exclusion reaches the siblings too: the rule on
			// disk.used_pct would be crit if it could read
			// disk.free_bytes, but that metric's check has no resolved
			// interval, so it is not in this item's Input at all. A rule
			// that cannot be evaluated does not match — the milestone's
			// binding invariant, reached here through the exclusion.
			name: "a tuple left out is not readable by its siblings either",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(diskRootT, 95, at(20), tenMin),
					snap(diskFreeRootT, 100, at(0), 0),
				},
				Effective: []rules.Effective{eff(diskSiblingRule(), diskRootT)},
				Previous: prev(
					was(diskRootT, rules.StateOK, at(0)),
					was(diskFreeRootT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: diskRootT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Staleness is "older than three intervals", so the deadline
			// itself is still fresh: the tuple is judged normally, and the
			// deadline is not a pending moment either, since it is not
			// ahead of now.
			name: "a value exactly three intervals old is not yet unknown",
			snap: rules.Snapshot{
				Now:       at(23),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), oneMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(23),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(23),
				}},
				Changes: []rules.Change{{
					At: at(23), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(23), EvaluatedAt: at(23)},
				NextDue: at(23).Add(15 * time.Second),
			},
		},
		{
			// … and when that is the only tuple there is, the host has no
			// tuples at all, so it writes no roll-up row rather than
			// holding the fleet page grey for ever.
			name: "a host whose only tuple has no resolved interval writes no host state",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(0), 0)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{NextDue: at(20).Add(15 * time.Second)},
		},
		{
			// The roll-up carries its Since from the previous roll-up, not
			// from the tuples: the host has stood at crit since minute 0,
			// even though neither tuple holding it there is that old.
			name: "the host roll-up carries its since from the previous roll-up",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(cpuT, 95, at(20), tenMin),
					snap(memT, 95, at(20), tenMin),
				},
				Effective: []rules.Effective{
					eff(cpuCrit(), cpuT),
					eff(memCrit(), memT),
				},
				Previous: prev(
					wasBy(cpuT, rules.StateCrit, at(8), critID),
					wasBy(memT, rules.StateCrit, at(5), memCritID),
				),
				PreviousHost: &rules.HostState{State: rules.StateCrit, Since: at(0)},
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: cpuT, State: rules.StateCrit, Since: at(8), RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: memT, State: rules.StateCrit, Since: at(5), RuleID: &memCritID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
				},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 2, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			name: "the host roll-up restarts its since when the host state changes",
			snap: rules.Snapshot{
				Now:          at(20),
				Tuples:       []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective:    []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:     prev(was(cpuT, rules.StateOK, at(0))),
				PreviousHost: &rules.HostState{State: rules.StateOK, Since: at(0)},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// The flag counts the change this evaluation is about to write:
			// four in the loaded window plus this one is five, so the tuple
			// is flagged on the tick that earned it, not the next one.
			name: "a change made by this evaluation counts towards flapping",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(0))),
				Changes30: map[rules.Tuple]int{cpuT: 4},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), Flapping: true, EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit,
					RuleID: &critID, Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// A tuple seen for the first time is an insert, not a
			// transition, so it writes no change — and contributes none to
			// the flapping count either.
			name: "a first evaluation does not count towards flapping",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Changes30: map[rules.Tuple]int{cpuT: 4},
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateCrit, Since: at(20),
					RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			name: "a recovery out of unknown takes effect at once",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 50, at(20), tenMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateUnknown, at(5))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(20),
					LastValue: ptr(50.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateUnknown, To: rules.StateOK,
					Value: ptr(50.0),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Data coming back bad is still a deterioration: the condition
			// has been observably true for zero seconds, so it ripens like
			// any other.
			name: "a deterioration out of unknown still has to ripen",
			snap: rules.Snapshot{
				Now:       at(20),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), tenMin), cpuT)},
				Previous:  prev(was(cpuT, rules.StateUnknown, at(5))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateUnknown, Since: at(5),
					LastValue: ptr(95.0), Candidate: rules.StateCrit, CandidateSince: ptr(at(20)),
					EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(5), EvaluatedAt: at(20)},
				NextDue: at(30),
			},
		},
		{
			// Step 7's carry-over: the host has stood at crit since the
			// earlier of the two tuples that hold it there, and neither
			// tuple moved this tick.
			name: "the host roll-up carries its since while the host state is unchanged",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(cpuT, 95, at(20), tenMin),
					snap(memT, 95, at(20), tenMin),
				},
				Effective: []rules.Effective{
					eff(cpuCrit(), cpuT),
					eff(memCrit(), memT),
				},
				Previous: prev(
					wasBy(cpuT, rules.StateCrit, at(8), critID),
					wasBy(memT, rules.StateCrit, at(5), memCritID),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: cpuT, State: rules.StateCrit, Since: at(8), RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
					{Tuple: memT, State: rules.StateCrit, Since: at(5), RuleID: &memCritID, LastValue: ptr(95.0), EvaluatedAt: at(20)},
				},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 2, Since: at(5), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// A host is only healthy from the moment its last unhealthy
			// tuple recovered, not from the oldest tuple that happens to
			// have been ok all along.
			name: "a healthy host dates its since from its last recovery",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(cpuT, 50, at(20), tenMin),
					snap(memT, 50, at(20), tenMin),
				},
				Effective: []rules.Effective{
					eff(cpuCrit(), cpuT),
					eff(memCrit(), memT),
				},
				Previous: prev(
					was(cpuT, rules.StateOK, at(0)),
					wasBy(memT, rules.StateCrit, at(5), memCritID),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: cpuT, State: rules.StateOK, Since: at(0), LastValue: ptr(50.0), EvaluatedAt: at(20)},
					{Tuple: memT, State: rules.StateOK, Since: at(20), LastValue: ptr(50.0), EvaluatedAt: at(20)},
				},
				Changes: []rules.Change{
					{At: at(20), Tuple: memT, From: rules.StateCrit, To: rules.StateOK, Value: ptr(50.0)},
				},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(20).Add(30 * time.Minute),
			},
		},
		{
			// A metric with no effective rule at all is ok, not unjudged:
			// the absence of a rule is a statement, and it is "nothing is
			// wrong here".
			name: "a tuple with no rule at all is ok",
			snap: rules.Snapshot{
				Now:      at(20),
				Tuples:   []rules.TupleSnapshot{snap(cpuLoadT, 7.5, at(20), tenMin)},
				Previous: prev(was(cpuLoadT, rules.StateOK, at(0))),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuLoadT, State: rules.StateOK, Since: at(0),
					LastValue: ptr(7.5), EvaluatedAt: at(20),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Review focus 7's other half: the rule that held the crit is
			// gone, so there is no clear condition left to hold it and the
			// next tick moves the tuple.
			name: "a crit whose deciding rule is gone is re-decided from scratch",
			snap: rules.Snapshot{
				Now:      at(20),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
				Previous: prev(wasBy(cpuT, rules.StateCrit, at(5), critID)),
			},
			want: rules.Result{
				States: []rules.TupleState{{
					Tuple: cpuT, State: rules.StateOK, Since: at(20),
					LastValue: ptr(95.0), EvaluatedAt: at(20),
				}},
				Changes: []rules.Change{{
					At: at(20), Tuple: cpuT, From: rules.StateCrit, To: rules.StateOK,
					Value: ptr(95.0),
				}},
				Host:    rules.HostState{State: rules.StateOK, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(50),
			},
		},
		{
			// Step 5 takes the earliest of every pending moment across
			// every tuple, whichever tuple it belongs to: here the earliest
			// is the ripening of the tuple listed last, behind two
			// staleness deadlines that are further out.
			name: "next due is the earliest pending moment across every tuple",
			snap: rules.Snapshot{
				Now: at(20),
				Tuples: []rules.TupleSnapshot{
					snap(memT, 50, at(20), 2*time.Minute),  // goes stale at minute 26
					snap(cpuLoadT, 0.5, at(20), time.Hour), // goes stale at minute 200
					snap(cpuT, 95, at(20), tenMin),         // ripens at minute 25
				},
				Effective: []rules.Effective{eff(withFor(cpuCrit(), 5*time.Minute), cpuT)},
				Previous: prev(
					was(cpuT, rules.StateOK, at(0)),
					was(memT, rules.StateOK, at(0)),
					was(cpuLoadT, rules.StateOK, at(0)),
				),
			},
			want: rules.Result{
				States: []rules.TupleState{
					{Tuple: memT, State: rules.StateOK, Since: at(0), LastValue: ptr(50.0), EvaluatedAt: at(20)},
					{Tuple: cpuLoadT, State: rules.StateOK, Since: at(0), LastValue: ptr(0.5), EvaluatedAt: at(20)},
					{Tuple: cpuT, State: rules.StateOK, Since: at(0), LastValue: ptr(95.0), Candidate: rules.StateCrit, CandidateSince: ptr(at(20)), EvaluatedAt: at(20)},
				},
				Host:    rules.HostState{State: rules.StateOK, Since: at(0), EvaluatedAt: at(20)},
				NextDue: at(25),
			},
		},
	}
}

// memCrit is the crit rule on the second metric the roll-up cases need.
func memCrit() rules.Rule {
	return evalRule(memCritID, memT, rules.SeverityCrit,
		compare(">", metric("memory.used_pct"), number(90)))
}

// diskWindowRule is the per-item window rule the window case judges with.
func diskWindowRule() rules.Rule {
	return evalRule(critID, diskRootT, rules.SeverityCrit,
		compare(">", window("disk.used_pct", "avg", 300), number(90)))
}

// diskSiblingRule reads disk.free_bytes while judging disk.used_pct — the
// reason Evaluate builds one Input per item rather than per tuple.
func diskSiblingRule() rules.Rule {
	return evalRule(critID, diskRootT, rules.SeverityCrit, and(
		compare(">", metric("disk.used_pct"), number(90)),
		compare("<", metric("disk.free_bytes"), number(1e9)),
	))
}

// TestWindowRequests pins what the store must fetch before Evaluate can
// run: one request per (item, window) a winning rule's trigger or clear
// condition names, deduplicated, and nothing at all for a rule whose tuple
// Evaluate will not judge — one the host does not report, or one whose
// check has no resolved interval and is therefore left out entirely.
func TestWindowRequests(t *testing.T) {
	trigger := evalRule(critID, diskRootT, rules.SeverityCrit,
		compare(">", window("disk.used_pct", "avg", 300), number(90)))
	clearing := withClear(trigger, compare("<", window("disk.used_pct", "min", 600), number(70)))
	// A second rule on the same item naming the very same window: the
	// request must appear once, not twice.
	sameWindow := evalRule(warnID, diskRootT, rules.SeverityWarn,
		compare(">", window("disk.used_pct", "avg", 300), number(80)))
	unreported := evalRule(memCritID, memT, rules.SeverityCrit,
		compare(">", window("memory.used_pct", "avg", 300), number(90)))

	s := rules.Snapshot{
		Now: at(20),
		Tuples: []rules.TupleSnapshot{
			snap(diskRootT, 95, at(20), tenMin),
			snap(diskVarT, 10, at(20), tenMin),
			snap(diskTmpT, 10, at(20), 0), // reported, but no resolved interval
		},
		Effective: []rules.Effective{
			eff(clearing, diskRootT),
			eff(sameWindow, diskRootT),
			eff(clearing, diskVarT),
			eff(clearing, diskTmpT),
			eff(unreported, memT),
		},
	}

	root := rules.ItemKey{CheckKey: "disk", Item: "/"}
	varr := rules.ItemKey{CheckKey: "disk", Item: "/var"}
	want := []rules.WindowRequest{
		{Key: root, Metric: "disk.used_pct", Agg: "avg", Seconds: 300},
		{Key: root, Metric: "disk.used_pct", Agg: "min", Seconds: 600},
		{Key: varr, Metric: "disk.used_pct", Agg: "avg", Seconds: 300},
		{Key: varr, Metric: "disk.used_pct", Agg: "min", Seconds: 600},
	}
	got := rules.WindowRequests(s)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("window requests:\n got %+v\nwant %+v", got, want)
	}
}

// TestWindowRequestsWithoutWindows guards the contract the ticker relies
// on: WindowRequests reads only Tuples and Effective, so it may be called
// on a Snapshot whose Windows field is still nil, and a rule set that
// names no window asks for nothing.
func TestWindowRequestsWithoutWindows(t *testing.T) {
	s := rules.Snapshot{
		Now:       at(20),
		Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
		Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
	}
	if got := rules.WindowRequests(s); got != nil {
		t.Fatalf("window requests: got %+v, want none", got)
	}
}

// TestEvaluateDoesNotMutateItsInput pins Evaluate's purity on the one
// surface a caller could be surprised by: the maps the Snapshot carries.
func TestEvaluateDoesNotMutateItsInput(t *testing.T) {
	s := rules.Snapshot{
		Now:       at(20),
		Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(20), tenMin)},
		Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
		Facts:     map[string]string{"cpu_count": "8"},
		Previous:  prev(was(cpuT, rules.StateOK, at(0))),
		Changes30: map[rules.Tuple]int{cpuT: 1},
	}
	before := fmt.Sprintf("%+v", s)
	rules.Evaluate(s)
	if after := fmt.Sprintf("%+v", s); after != before {
		t.Fatalf("Evaluate mutated its snapshot:\nbefore %s\nafter  %s", before, after)
	}
}

// assertResult compares the whole Result — every case asserts states,
// changes, roll-up and NextDue, never only the field it is named after.
func assertResult(t *testing.T, got, want rules.Result) {
	t.Helper()
	if len(got.States) != len(want.States) {
		t.Fatalf("states: got %d, want %d\n got %s\nwant %s",
			len(got.States), len(want.States), fmtStates(got.States), fmtStates(want.States))
	}
	for i := range want.States {
		if !reflect.DeepEqual(got.States[i], want.States[i]) {
			t.Errorf("state %d:\n got %s\nwant %s", i, fmtState(got.States[i]), fmtState(want.States[i]))
		}
	}
	if len(got.Changes) != len(want.Changes) {
		t.Fatalf("changes: got %d, want %d\n got %s\nwant %s",
			len(got.Changes), len(want.Changes), fmtChanges(got.Changes), fmtChanges(want.Changes))
	}
	for i := range want.Changes {
		if !reflect.DeepEqual(got.Changes[i], want.Changes[i]) {
			t.Errorf("change %d:\n got %s\nwant %s", i, fmtChange(got.Changes[i]), fmtChange(want.Changes[i]))
		}
	}
	if !reflect.DeepEqual(got.Host, want.Host) {
		t.Errorf("host:\n got %s\nwant %s", fmtHost(got.Host), fmtHost(want.Host))
	}
	if !got.NextDue.Equal(want.NextDue) {
		t.Errorf("next due: got %s, want %s", clock(got.NextDue), clock(want.NextDue))
	}
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("15:04:05")
}

func clockPtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return clock(*t)
}

func idStr(id *uuid.UUID) string {
	if id == nil {
		return "-"
	}
	switch *id {
	case critID:
		return "crit-rule"
	case warnID:
		return "warn-rule"
	case memCritID:
		return "mem-crit-rule"
	default:
		return id.String()
	}
}

func numStr(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%g", *v)
}

func txtStr(v *string) string {
	if v == nil {
		return "-"
	}
	return *v
}

func fmtState(s rules.TupleState) string {
	return fmt.Sprintf("%s/%s/%s state=%s since=%s rule=%s value=%s label=%s cand=%q@%s flap=%t eval=%s",
		s.Tuple.CheckKey, s.Tuple.Item, s.Tuple.Metric, s.State, clock(s.Since), idStr(s.RuleID),
		numStr(s.LastValue), txtStr(s.LastLabel), s.Candidate, clockPtr(s.CandidateSince),
		s.Flapping, clock(s.EvaluatedAt))
}

func fmtChange(c rules.Change) string {
	return fmt.Sprintf("%s/%s/%s %s->%s at=%s rule=%s value=%s label=%s",
		c.Tuple.CheckKey, c.Tuple.Item, c.Tuple.Metric, c.From, c.To, clock(c.At),
		idStr(c.RuleID), numStr(c.Value), txtStr(c.Label))
}

func fmtHost(h rules.HostState) string {
	return fmt.Sprintf("state=%s crit=%d warn=%d unknown=%d since=%s eval=%s",
		h.State, h.Crit, h.Warn, h.Unknown, clock(h.Since), clock(h.EvaluatedAt))
}

func fmtStates(states []rules.TupleState) string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = fmtState(s)
	}
	return "[" + strings.Join(out, "; ") + "]"
}

func fmtChanges(changes []rules.Change) string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = fmtChange(c)
	}
	return "[" + strings.Join(out, "; ") + "]"
}
