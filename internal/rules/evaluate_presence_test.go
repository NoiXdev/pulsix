package rules_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// Client hosts (backlog 3.57, docs/superpowers/specs/2026-11-13-client-hosts-design.md,
// "Evaluation"): a client whose agent is off is frozen — the evaluation
// changes nothing — and a client whose agent is back is judged with each
// tuple's staleness measured from max(reading, connection start). A server,
// and a client without an agent, are judged exactly as before; the rows
// below that say so mirror the table in evaluate_test.go.

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

// offClient is a client whose agent was last seen at lastSeen and is not
// online, with the absent warning after absentAfter (0 = off).
func offClient(lastSeen time.Time, absentAfter time.Duration) rules.HostPresence {
	return rules.HostPresence{
		Client: true, HasAgent: true, AgentOnline: false,
		AgentLastSeen: lastSeen, AgentConnected: lastSeen.Add(-day), AbsentAfter: absentAfter,
	}
}

// onClient is a client whose agent is online and connected since connected
// (zero = unknown).
func onClient(connected time.Time) rules.HostPresence {
	return rules.HostPresence{
		Client: true, HasAgent: true, AgentOnline: true,
		AgentLastSeen: connected, AgentConnected: connected, AbsentAfter: 14 * day,
	}
}

type presenceCase struct {
	name string
	snap rules.Snapshot
	want rules.Result
	// The three fields assertResult does not look at.
	frozen   bool
	absent   bool
	lastSeen time.Time
}

func TestEvaluateClientPresence(t *testing.T) {
	crit := rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(-600), EvaluatedAt: at(-500)}
	// A frozen client's inputs: a stale crit tuple that today's rule would
	// turn unknown, an unmeasured tuple and an ignored one that would
	// otherwise be reported, and a stored roll-up.
	frozenInputs := func(p rules.HostPresence, previousHost *rules.HostState) rules.Snapshot {
		return rules.Snapshot{
			Now: at(0),
			Tuples: []rules.TupleSnapshot{
				snap(cpuT, 95, at(-500), oneMin),
				{Tuple: memT, Value: ptr(10.0), At: at(-500)}, // no resolved interval
				snap(diskVarT, 99, at(-500), oneMin),
			},
			Effective:    []rules.Effective{eff(cpuCrit(), cpuT), eff(diskVarCrit(), diskVarT)},
			Previous:     prev(wasBy(cpuT, rules.StateCrit, at(-600), critID), was(memT, rules.StateOK, at(-600)), was(diskVarT, rules.StateOK, at(-600))),
			Changes30:    map[rules.Tuple]int{},
			PreviousHost: previousHost,
			Ignored:      map[rules.ItemKey]bool{{CheckKey: "disk", Item: "/var"}: true},
			Presence:     p,
		}
	}

	cases := []presenceCase{
		{
			// Spec item 1: nothing moves — no state, no change, no
			// unmeasured or dropped report, the roll-up carried as it is.
			// The absent warning is off, so the next look is the silent
			// heartbeat.
			name:   "a frozen client changes nothing and comes back on the silent heartbeat",
			snap:   frozenInputs(offClient(at(-500), 0), &crit),
			want:   rules.Result{Host: crit, NextDue: at(5)},
			frozen: true, lastSeen: at(-500),
		},
		{
			name:   "a frozen client never evaluated keeps having no roll-up",
			snap:   frozenInputs(offClient(at(-500), 0), nil),
			want:   rules.Result{NextDue: at(5)},
			frozen: true, lastSeen: at(-500),
		},
		{
			// Spec item 3: the ticker comes back exactly when the warning
			// is due, when that is sooner than the heartbeat.
			name:   "a frozen client is due again at its absent deadline when that comes first",
			snap:   frozenInputs(offClient(at(-14*24*60+2), 14*day), &crit),
			want:   rules.Result{Host: crit, NextDue: at(2)},
			frozen: true, lastSeen: at(-14*24*60 + 2),
		},
		{
			// The 5-minute cap: a deadline days away still brings the
			// ticker back on the heartbeat (a failed post-commit kind mark,
			// an agent reconnecting without sending data).
			name:   "a frozen client's due time is capped at the silent heartbeat",
			snap:   frozenInputs(offClient(at(-2*24*60), 14*day), &crit),
			want:   rules.Result{Host: crit, NextDue: at(5)},
			frozen: true, lastSeen: at(-2 * 24 * 60),
		},
		{
			name:   "a frozen client is absent from the moment the threshold is reached",
			snap:   frozenInputs(offClient(at(-14*24*60), 14*day), &crit),
			want:   rules.Result{Host: crit, NextDue: at(5)},
			frozen: true, absent: true, lastSeen: at(-14 * 24 * 60),
		},
		{
			name:   "a frozen client past the threshold stays absent on the heartbeat",
			snap:   frozenInputs(offClient(at(-15*24*60), 14*day), &crit),
			want:   rules.Result{Host: crit, NextDue: at(5)},
			frozen: true, absent: true, lastSeen: at(-15 * 24 * 60),
		},
		{
			name:   "with the warning off a frozen client is never absent",
			snap:   frozenInputs(offClient(at(-400*24*60), 0), &crit),
			want:   rules.Result{Host: crit, NextDue: at(5)},
			frozen: true, lastSeen: at(-400 * 24 * 60),
		},
		{
			// Spec item 2: a 1-hour check read long before the outage is
			// not unknown until three intervals after the agent connected.
			name: "an online client's tuple read before the connection is stale from the connection start",
			snap: rules.Snapshot{
				Now:      at(30),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 50, at(-1000), hour)},
				Previous: prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence: onClient(at(0)),
			},
			want: rules.Result{
				States: []rules.TupleState{{Tuple: cpuT, State: rules.StateOK, Since: at(-2000), LastValue: ptr(50.0), EvaluatedAt: at(30)}},
				Host:   rules.HostState{State: rules.StateOK, Since: at(-2000), EvaluatedAt: at(30)},
				// connected_since + 3 x 1 h.
				NextDue: at(180),
			},
		},
		{
			name: "an online client's tuple goes unknown three intervals after the connection start",
			snap: rules.Snapshot{
				Now:      at(181),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 50, at(-1000), hour)},
				Previous: prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence: onClient(at(0)),
			},
			want: rules.Result{
				States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateUnknown, Since: at(181), LastValue: ptr(50.0), EvaluatedAt: at(181)}},
				Changes: []rules.Change{{At: at(181), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(50.0)}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(181), EvaluatedAt: at(181)},
				NextDue: at(181).Add(15 * time.Second),
			},
		},
		{
			// max, not the connection start alone: a reading after the
			// connection is measured from itself.
			name: "an online client's reading after the connection start is measured from the reading",
			snap: rules.Snapshot{
				Now:      at(20),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 50, at(10), oneMin)},
				Previous: prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence: onClient(at(0)),
			},
			want: rules.Result{
				States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateUnknown, Since: at(20), LastValue: ptr(50.0), EvaluatedAt: at(20)}},
				Changes: []rules.Change{{At: at(20), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(50.0)}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(20), EvaluatedAt: at(20)},
				NextDue: at(20).Add(15 * time.Second),
			},
		},
		{
			// Buffered results arriving on reconnect are ordinary readings:
			// once online they are judged like any other.
			name: "an online client's fresh reading is judged by its rules",
			snap: rules.Snapshot{
				Now:       at(30),
				Tuples:    []rules.TupleSnapshot{snap(cpuT, 95, at(29), oneMin)},
				Effective: []rules.Effective{eff(cpuCrit(), cpuT)},
				Previous:  prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence:  onClient(at(0)),
			},
			want: rules.Result{
				States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateCrit, Since: at(30), RuleID: &critID, LastValue: ptr(95.0), EvaluatedAt: at(30)}},
				Changes: []rules.Change{{At: at(30), Tuple: cpuT, From: rules.StateOK, To: rules.StateCrit, RuleID: &critID, Value: ptr(95.0)}},
				Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: at(30), EvaluatedAt: at(30)},
				NextDue: at(32),
			},
		},
	}

	// Today's rule, unchanged: the reading alone anchors staleness. The
	// same inputs as the connection-start case above, under every
	// presence that must not move the anchor or freeze anything.
	todaysRule := rules.Result{
		States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateUnknown, Since: at(30), LastValue: ptr(50.0), EvaluatedAt: at(30)}},
		Changes: []rules.Change{{At: at(30), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(50.0)}},
		Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(30), EvaluatedAt: at(30)},
		NextDue: at(30).Add(15 * time.Second),
	}
	unchanged := []struct {
		name string
		p    rules.HostPresence
	}{
		{"a server with no agent", rules.HostPresence{}},
		{"a server whose agent is off", rules.HostPresence{HasAgent: true, AgentLastSeen: at(-1000), AgentConnected: at(0), AbsentAfter: day}},
		{"a server whose agent is online", rules.HostPresence{HasAgent: true, AgentOnline: true, AgentLastSeen: at(29), AgentConnected: at(0)}},
		{"a client without an agent", rules.HostPresence{Client: true, AbsentAfter: day}},
		{"an online client whose connection start is unknown", onClient(time.Time{})},
	}
	for _, u := range unchanged {
		cases = append(cases, presenceCase{
			name: u.name + " is judged by today's rule",
			snap: rules.Snapshot{
				Now:      at(30),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 50, at(-1000), hour)},
				Previous: prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence: u.p,
			},
			want: todaysRule,
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.Evaluate(tc.snap)
			assertResult(t, got, tc.want)
			if got.Frozen != tc.frozen || got.Absent != tc.absent || !got.AgentLastSeen.Equal(tc.lastSeen) {
				t.Errorf("frozen=%v absent=%v last seen=%s, want frozen=%v absent=%v last seen=%s",
					got.Frozen, got.Absent, clock(got.AgentLastSeen), tc.frozen, tc.absent, clock(tc.lastSeen))
			}
		})
	}
}

// TestWindowRequestsFrozen: a frozen client judges nothing, so no window
// is fetched for it — the same reasoning as an ignored item's (3.39). Once
// its agent is online the request is back.
func TestWindowRequestsFrozen(t *testing.T) {
	r := evalRule(critID, diskRootT, rules.SeverityCrit,
		compare(">", window("disk.used_pct", "avg", 300), number(90)))
	s := rules.Snapshot{
		Now:       at(20),
		Tuples:    []rules.TupleSnapshot{snap(diskRootT, 95, at(20), tenMin)},
		Effective: []rules.Effective{eff(r, diskRootT)},
		Presence:  offClient(at(10), 0),
	}
	if got := rules.WindowRequests(s); len(got) != 0 {
		t.Fatalf("window requests of a frozen client = %+v, want none", got)
	}
	s.Presence.AgentOnline = true
	if got := rules.WindowRequests(s); len(got) != 1 {
		t.Fatalf("window requests of an online client = %+v, want the rule's one", got)
	}
}
