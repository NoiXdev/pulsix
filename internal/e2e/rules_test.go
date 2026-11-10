// The v0.3b-2 milestone gate (backlog 3.4a, second half): the rule
// pipeline from a crossed threshold to an unknown host, end to end, on the
// same real-agent harness agent_test.go and checks_test.go already wire —
// same manager wiring, same real agent binary loop, same operator driving
// everything through the HTTP API the GUI uses.
//
// The four things this gate exists to prove, in one scenario:
//
//  1. A threshold the host actually crosses reaches warn — no rule fires
//     on a value nobody reported.
//  2. A deterioration that holds long enough ripens into crit exactly when
//     for_duration says it should, not before — checked as a lower bound
//     on wall-clock time elapsed since the threshold's own edit, not only
//     as "crit eventually appears": an implementation that promoted on
//     the very next tick regardless of for_duration would pass "eventually
//     crit" just as well, faster, and only the timing check catches it.
//  3. An improvement is never held back by for_duration: the moment a
//     recovering value applies, ok returns at once.
//  4. An agent that stops reporting altogether leaves its host unknown,
//     once — and only once — three of its check's own intervals have
//     passed with nothing new, checked the same way as claim 2: a lower
//     bound on elapsed time since the agent's own last reading, not only
//     that unknown eventually appears.
//
// How the crossing and the recovery are driven, and why not otherwise:
// internal/agent/checks.Registry wires the agent's own, real cpu plugin
// with no seam for a synthetic reading (the same "nothing is faked on
// either side" posture agent_test.go's own header states), so the value
// this scenario judges is always this machine's genuine, gopsutil-reported
// cpu.used_pct — never a fake. What the test drives is which side of it
// the rule's own threshold sits on, the same lever an operator has:
//   - warn's threshold is set below any possible percentage (an
//     always-true trigger) so it fires the instant the tuple is first
//     evaluated — an operator setting a threshold their fleet is already
//     over is exactly why "warn immediately" exists.
//   - crit starts with a threshold no real percentage reaches, then this
//     test edits it (PUT, the same route the rule editor's "save" calls)
//     down to another always-true value — from the evaluator's own
//     perspective (states.go's decide, matching the CURRENT tree against
//     the CURRENT value on every tick) this is indistinguishable from the
//     host's own value climbing past a fixed bar, and it is what makes
//     claim 2 deterministic rather than at the mercy of this machine's own
//     background CPU noise. An earlier version of this gate tried to drive
//     the actual percentage up and down with induced load; a throwaway
//     measurement on the very machine this suite runs on showed genuine
//     idle background noise alone at 20-32% cpu.used_pct with no induced
//     load whatsoever — high enough to cross a "moderate load" band chosen
//     with real margin either side, on a machine doing nothing this test
//     asked it to. A threshold gate that depends on how busy the host
//     happens to be underneath the test runner is not a gate; editing the
//     threshold against the real, unmodified value is.
//   - Recovery raises both thresholds back out of reach, the same lever in
//     the other direction; decide's own step 4 (states.go) applies an
//     improvement at once, with no ripening, so ok is expected the moment
//     the next evaluation tick sees the edit.
//
// A host-bound cpu check of its own (interval_seconds: 10, the shortest
// internal/checks accepts) is what keeps claim 4's staleness deadline
// (three of that interval) in seconds rather than the seed cpu check's own
// three minutes.
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ruleCheckInterval is the host-bound cpu check's own interval: the
// minimum internal/checks.MinInterval accepts, so claim 4's staleness
// deadline (3x this) is 30 seconds, not three minutes.
const ruleCheckInterval = 10 * time.Second

// belowAnyPercentage and aboveAnyPercentage are thresholds no real
// cpu.used_pct reading (0-100) can ever fail, respectively satisfy — the
// "always crosses" and "never crosses" ends of the lever this gate's
// header comment explains. ruleForCrit is how long crit's own
// deterioration must hold before it counts.
const (
	belowAnyPercentage = -1.0
	aboveAnyPercentage = 1_000_000.0
	ruleForCrit        = 20 * time.Second
)

// ripenRaceMargin bounds claim 2's lower-bound check against one specific,
// narrow race in the evaluation ticker's own ordering (ticker.go's Tick):
// it reads its own "now" BEFORE it loads the rules a host is judged
// against, so a PUT that commits in the brief window between those two
// reads is picked up by a tick whose own "now" — and so whose
// candidate_since — predates this test's own measurement of when that PUT
// finished. That window is bounded by one host's claim-and-load latency,
// which is well under a second; this margin is generous against it without
// being anywhere near loose enough to hide a real for_duration bug (an
// implementation that ignored for_duration and promoted on the very next
// tick would still miss the lower bound by most of ruleForCrit's own 20s,
// not by a couple of seconds).
const ripenRaceMargin = 2 * time.Second

// ruleTupleOut is the fields of GET /hosts/{id}/checks' value entries
// checks_test.go's own checkValueOut does not need for its own gate: the
// rule engine's verdict (v0.3b-2 Task 11's additive fields,
// host_check_handlers.go's hostCheckValueDTO), plus the tuple's own At —
// carried by that DTO already (v0.3b-1) but not by checkValueOut's own
// pin of it — which this gate's claim 4 needs to compute staleness from
// the agent's actual last reading, not from when the test called stop.
// A second, narrower struct rather than widening the v0.3b-1 one: that
// type is this package's own pin of the v0.3b-1 contract.
type ruleTupleOut struct {
	Metric string    `json:"metric"`
	Value  *float64  `json:"value"`
	State  *string   `json:"state"`
	At     time.Time `json:"at"`
}

type ruleHostCheckOut struct {
	Key    string         `json:"key"`
	Values []ruleTupleOut `json:"values"`
}

// cpuTupleState reads the host page's own monitoring section for the
// whole-host cpu.used_pct tuple's current state, last value and last
// reading time — the same route checks_test.go's hostChecks reads,
// decoded into the shape that also carries Task 11's verdict.
func cpuTupleState(t *testing.T, w agentWorld, hostID string) (state *string, value *float64, at time.Time) {
	t.Helper()
	var out struct {
		Checks []ruleHostCheckOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/checks", nil, &out)
	for _, c := range out.Checks {
		if c.Key != "cpu" {
			continue
		}
		for _, v := range c.Values {
			if v.Metric == cpuMetric {
				return v.State, v.Value, v.At
			}
		}
	}
	return nil, nil, time.Time{}
}

// waitForTupleState polls the host page until the cpu.used_pct tuple
// reads want, the way an operator watching the host page would — nothing
// pushes this either. what names the phase being waited for, for a
// timeout's own failure message. It returns the moment the wanted state
// was first observed (wall-clock, this process' own clock) and the
// tuple's own At at that moment, so a caller can assert a transition did
// not happen too early as well as that it happened at all.
func waitForTupleState(t *testing.T, w agentWorld, hostID, want, what string, within time.Duration) (observedAt time.Time, tupleAt time.Time) {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastState *string
	var lastValue *float64
	for time.Now().Before(deadline) {
		var lastAt time.Time
		lastState, lastValue, lastAt = cpuTupleState(t, w, hostID)
		if lastState != nil && *lastState == want {
			return time.Now(), lastAt
		}
		time.Sleep(500 * time.Millisecond)
	}
	gotState, gotValue := "<none>", "<none>"
	if lastState != nil {
		gotState = *lastState
	}
	if lastValue != nil {
		gotValue = fmt.Sprintf("%.1f", *lastValue)
	}
	t.Fatalf("timed out waiting for %s (cpu.used_pct state %q); last state %s, last value %s%%",
		what, want, gotState, gotValue)
	return time.Time{}, time.Time{}
}

// ruleOut is the create/update response's rule id — the only field this
// gate reads back, to PUT the same rule again with a different threshold.
type ruleOut struct {
	Rule struct {
		ID string `json:"id"`
	} `json:"rule"`
}

// createFormRule creates one host-scoped, form-mode rule over cpuMetric
// and returns its id.
func createFormRule(t *testing.T, w agentWorld, hostID, name, severity string, threshold float64, forSeconds int) string {
	t.Helper()
	var out ruleOut
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/rules", map[string]any{
		"name": name, "enabled": true, "scope": "host", "host_id": hostID,
		"metric": cpuMetric, "severity": severity, "mode": "form",
		"form":        map[string]any{"metric": cpuMetric, "op": ">", "value": threshold},
		"for_seconds": forSeconds,
	}, &out)
	return out.Rule.ID
}

// putFormThreshold replaces ruleID's threshold in place — the same full
// PUT the rule editor's "save" sends, and the lever this gate's header
// comment explains: what looks like a value crossing to the evaluator can
// equally be the threshold moving underneath an unchanged, real value.
func putFormThreshold(t *testing.T, w agentWorld, ruleID, hostID, name, severity string, threshold float64, forSeconds int) {
	t.Helper()
	w.api.do(http.MethodPut, "/api/v1/orgs/"+w.org+"/rules/"+ruleID, map[string]any{
		"name": name, "enabled": true, "scope": "host", "host_id": hostID,
		"metric": cpuMetric, "severity": severity, "mode": "form",
		"form":        map[string]any{"metric": cpuMetric, "op": ">", "value": threshold},
		"for_seconds": forSeconds,
	}, nil)
}

func TestRulePipelineGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx)

	// --- Setup: an agent, and a short-interval check definition of its
	//     own ------------------------------------------------------------
	// Not deferred: claim 4 stops this agent explicitly, partway through
	// the scenario, the same way agent_test.go's own revocation act stops
	// its agent early rather than waiting for the test to return.
	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-rules"), "e2e-rules")
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/checks", map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": int(ruleCheckInterval / time.Second),
		"enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)
	if got := hostCheck(t, w.hostChecks(t, hostID), "cpu").IntervalSeconds; got != int(ruleCheckInterval/time.Second) {
		t.Fatalf("the host's effective cpu interval is %ds after its own definition, want %d", got, int(ruleCheckInterval/time.Second))
	}

	// Two rules over the same metric, at this host's own scope: warn's
	// threshold is already crossed by any reading at all (for_duration 0,
	// so nothing ripens); crit's threshold starts out of reach.
	warnID := createFormRule(t, w, hostID, "e2e-cpu-warn", "warn", belowAnyPercentage, 0)
	critID := createFormRule(t, w, hostID, "e2e-cpu-crit", "crit", aboveAnyPercentage, int(ruleForCrit/time.Second))

	// --- 1. A crossed threshold reaches warn ------------------------------
	waitForTupleState(t, w, hostID, "warn", "warn's always-crossed threshold", 60*time.Second)

	// --- 2. A deterioration held long enough ripens into crit -------------
	// crit's threshold moves to one every reading crosses too — the same
	// PUT the rule editor's own "edit your threshold" sends (Playwright's
	// rules.spec.ts exercises that surface directly). The evaluator reads
	// the tree fresh on every tick, so from here on crit's trigger matches
	// continuously, exactly as a genuinely rising value would, and the
	// candidate started by this edit must hold, unbroken, for the whole of
	// ruleForCrit before it is promoted (states.go's decide, step 4).
	//
	// ripenedAt is read right after the PUT's HTTP round trip completes —
	// after the rule's new threshold has already committed, since the
	// handler writes before it answers — so it is this test's own honest
	// lower bound on when candidate_since could first have been set.
	// Asserting the observed promotion is no earlier than ripenedAt +
	// ruleForCrit (minus ripenRaceMargin) is what actually pins "not
	// before for_duration ripens", not only "eventually crit": a rule
	// engine that ignored for_duration and promoted on the next tick would
	// fail this assertion by most of ruleForCrit's own 20 seconds.
	putFormThreshold(t, w, critID, hostID, "e2e-cpu-crit", "crit", belowAnyPercentage, int(ruleForCrit/time.Second))
	ripenedAt := time.Now() // after the PUT's own commit, not before it
	observedCrit, _ := waitForTupleState(t, w, hostID, "crit", "crit's threshold ripening over ruleForCrit", ruleForCrit+60*time.Second)
	if elapsed := observedCrit.Sub(ripenedAt); elapsed < ruleForCrit-ripenRaceMargin {
		t.Fatalf("crit was reached %s after the threshold's own PUT, want at least %s (ruleForCrit %s minus the %s race margin): "+
			"for_duration looks like it was not honoured", elapsed, ruleForCrit-ripenRaceMargin, ruleForCrit, ripenRaceMargin)
	}

	// --- 3. A recovering value returns to ok at once -----------------------
	// Both thresholds move back out of reach. Unlike the climb above,
	// nothing here has to hold for any duration first — decide's step 4 is
	// downward-only — so the very next evaluation tick that sees the edit
	// must already read ok.
	putFormThreshold(t, w, warnID, hostID, "e2e-cpu-warn", "warn", aboveAnyPercentage, 0)
	putFormThreshold(t, w, critID, hostID, "e2e-cpu-crit", "crit", aboveAnyPercentage, int(ruleForCrit/time.Second))
	waitForTupleState(t, w, hostID, "ok", "recovery, which for_duration must never delay", 60*time.Second)

	// --- 4. A host that stops reporting goes unknown -----------------------
	// Nothing pushes this: the last known value simply ages past its
	// staleness deadline (3x ruleCheckInterval, states.go's staleAt), and
	// the evaluation ticker's own heartbeat (or the deadline the previous
	// evaluation already scheduled, states.go's nextDue) is what brings
	// the ticker back to notice.
	stop()
	observedUnknown, lastSampleAt := waitForTupleState(t, w, hostID, "unknown", "the stopped agent's tuple going stale",
		3*ruleCheckInterval+90*time.Second)
	// lastSampleAt is the agent's own timestamp for its last real reading —
	// read back from the same response that first showed unknown, so it is
	// exactly the ts.At staleAt was computed from (states.go). Unlike
	// claim 2's PUT, there is no ordering race to margin against here:
	// lastSampleAt was written well before stop() was even called, and a
	// tick can only promote to unknown once its own "now" already exceeds
	// lastSampleAt + 3x the interval — so this bound holds with no slack,
	// and an implementation using, say, 1x the interval instead of 3x
	// would miss it by a full two intervals.
	if elapsed := observedUnknown.Sub(lastSampleAt); elapsed < 3*ruleCheckInterval {
		t.Fatalf("the tuple went unknown %s after its own last reading, want at least %s (3x ruleCheckInterval): "+
			"staleness looks like it was not honoured", elapsed, 3*ruleCheckInterval)
	}

	// The fleet overview agrees: this is the only host in the
	// organization, so its roll-up is the whole page.
	var fleet struct {
		Hosts []struct {
			HostID string  `json:"host_id"`
			State  *string `json:"state"`
		} `json:"hosts"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/fleet", nil, &fleet)
	if len(fleet.Hosts) != 1 || fleet.Hosts[0].HostID != hostID || fleet.Hosts[0].State == nil || *fleet.Hosts[0].State != "unknown" {
		t.Fatalf("fleet overview = %+v, want the one host at unknown", fleet.Hosts)
	}
}
