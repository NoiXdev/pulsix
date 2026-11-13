package rules_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// Client hosts (backlog 3.57, docs/superpowers/specs/2026-11-13-client-hosts-design.md,
// "Evaluation"): a client whose agent is off is frozen — the evaluation
// changes nothing — and a client whose agent is back is judged with each
// tuple's staleness measured from max(reading, online start). A server,
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
		AgentLastSeen: lastSeen, AgentOnlineSince: lastSeen.Add(-day), AbsentAfter: absentAfter,
	}
}

// onClient is a client whose agent is online, in an online period that
// began at onlineSince (zero = unknown).
func onClient(onlineSince time.Time) rules.HostPresence {
	return rules.HostPresence{
		Client: true, HasAgent: true, AgentOnline: true,
		AgentLastSeen: onlineSince, AgentOnlineSince: onlineSince, AbsentAfter: 14 * day,
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
			// Spec item 2, the overnight case: a 1-hour check last read
			// ten hours ago, before the notebook was shut down, is not
			// unknown until three intervals after the agent came back
			// online at minute 0.
			name: "an online client's tuple read before an overnight power-off is stale from the online start",
			snap: rules.Snapshot{
				Now:      at(30),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 50, at(-600), hour)},
				Previous: prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence: onClient(at(0)),
			},
			want: rules.Result{
				States: []rules.TupleState{{Tuple: cpuT, State: rules.StateOK, Since: at(-2000), LastValue: ptr(50.0), EvaluatedAt: at(30)}},
				Host:   rules.HostState{State: rules.StateOK, Since: at(-2000), EvaluatedAt: at(30)},
				// online_since + 3 x 1 h.
				NextDue: at(180),
			},
		},
		{
			name: "an online client's tuple goes unknown three intervals after the online start",
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
			// max, not the online start alone: a reading after it is
			// measured from itself.
			name: "an online client's reading after the online start is measured from the reading",
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
	// same inputs as the online-start case above, under every
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
		{"a server whose agent is off", rules.HostPresence{HasAgent: true, AgentLastSeen: at(-1000), AgentOnlineSince: at(0), AbsentAfter: day}},
		{"a server whose agent is online", rules.HostPresence{HasAgent: true, AgentOnline: true, AgentLastSeen: at(29), AgentOnlineSince: at(0)}},
		{"a client without an agent", rules.HostPresence{Client: true, AbsentAfter: day}},
		{"an online client whose online start is unknown", onClient(time.Time{})},
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

// TestEvaluateWakesAnOnlineClientWhenItsUnknownGraceEnds (backlog 3.57,
// the shutdown race): an online client's unknown roll-up opens a host
// alert only once it has read unknown for the agents list's online window
// (alerts.Reconcile, AlertInput.UnknownGrace) — by then a client that was
// shutting down is frozen. The ticker must look again when that grace
// ends, not on the five-minute silent heartbeat an all-unknown host
// otherwise falls back to. The due time is only ever tightened: a
// heartbeat that comes first stays.
func TestEvaluateWakesAnOnlineClientWhenItsUnknownGraceEnds(t *testing.T) {
	const window = 90 * time.Second
	client := onClient(at(0))
	client.OnlineWindow = window
	reconnectedClient := onClient(at(182))
	reconnectedClient.OnlineWindow = window

	// A 1-hour check last read before the online period began: stale at
	// online start + 3 h = at(180).
	silentPass := func(now time.Time, p rules.HostPresence) rules.Snapshot {
		return rules.Snapshot{
			Now:          now,
			Tuples:       []rules.TupleSnapshot{snap(cpuT, 50, at(-1000), hour)},
			Previous:     prev(was(cpuT, rules.StateUnknown, at(181))),
			PreviousHost: &rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(181), EvaluatedAt: at(181)},
			Presence:     p,
		}
	}
	silentResult := func(now time.Time, due time.Time) rules.Result {
		return rules.Result{
			States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateUnknown, Since: at(181), LastValue: ptr(50.0), EvaluatedAt: now}},
			Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(181), EvaluatedAt: now},
			NextDue: due,
		}
	}

	cases := []struct {
		name string
		snap rules.Snapshot
		want rules.Result
	}{
		{
			// The pass that turns the roll-up unknown keeps its 15-second
			// heartbeat: it comes before the grace's end.
			name: "the pass that goes unknown keeps the heartbeat that comes first",
			snap: rules.Snapshot{
				Now:      at(181),
				Tuples:   []rules.TupleSnapshot{snap(cpuT, 50, at(-1000), hour)},
				Previous: prev(was(cpuT, rules.StateOK, at(-2000))),
				Presence: client,
			},
			want: rules.Result{
				States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateUnknown, Since: at(181), LastValue: ptr(50.0), EvaluatedAt: at(181)}},
				Changes: []rules.Change{{At: at(181), Tuple: cpuT, From: rules.StateOK, To: rules.StateUnknown, Value: ptr(50.0)}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(181), EvaluatedAt: at(181)},
				NextDue: at(181).Add(15 * time.Second),
			},
		},
		{
			name: "a silent pass inside the grace is due when the grace ends, not on the silent heartbeat",
			snap: silentPass(at(182), client),
			want: silentResult(at(182), at(181).Add(window)),
		},
		{
			name: "once the grace is over the silent heartbeat applies",
			snap: silentPass(at(183), client),
			want: silentResult(at(183), at(188)),
		},
		{
			// Back from an outage with the roll-up carried unknown through
			// the freeze (a probe still failing): the grace runs from the
			// start of the online period, so the ticker returns when it
			// ends rather than five minutes on.
			name: "a client back online with a carried unknown roll-up is due when the grace from its online start ends",
			snap: rules.Snapshot{
				Now:          at(182).Add(10 * time.Second),
				Tuples:       []rules.TupleSnapshot{{Tuple: cpuT, Err: "probe failed", At: at(182), Interval: oneMin}},
				Previous:     prev(was(cpuT, rules.StateUnknown, at(100))),
				PreviousHost: &rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(100), EvaluatedAt: at(100)},
				Presence:     reconnectedClient,
			},
			want: rules.Result{
				States:  []rules.TupleState{{Tuple: cpuT, State: rules.StateUnknown, Since: at(100), EvaluatedAt: at(182).Add(10 * time.Second)}},
				Host:    rules.HostState{State: rules.StateUnknown, Unknown: 1, Since: at(100), EvaluatedAt: at(182).Add(10 * time.Second)},
				NextDue: at(182).Add(window),
			},
		},
		{
			name: "a server has no grace and keeps the silent heartbeat",
			snap: silentPass(at(182), rules.HostPresence{HasAgent: true, AgentOnline: true, AgentLastSeen: at(181)}),
			want: silentResult(at(182), at(187)),
		},
		{
			name: "a client without an agent has no grace and keeps the silent heartbeat",
			snap: silentPass(at(182), rules.HostPresence{Client: true, OnlineWindow: window}),
			want: silentResult(at(182), at(187)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertResult(t, rules.Evaluate(tc.snap), tc.want)
		})
	}
}
