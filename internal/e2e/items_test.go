// The v0.3b-3 milestone gate: the item decision pipeline end to end, on the
// same real-agent harness agent_test.go, checks_test.go and rules_test.go
// already wire — same manager wiring, same real agent binary loop, same
// operator driving everything through the HTTP API the GUI uses.
//
// The arc this gate proves, in one scenario:
//
//  1. An agent reports a mount, and an item-scoped rule over it crosses
//     its threshold: the host page's own tuple reaches crit.
//  2. An operator ignores that item: the very next evaluation leaves the
//     host ok again, and the tuple itself carries no state — only
//     Ignored, never a fabricated "ok" (items.Decide's own contract,
//     Apply's Dropped rows, see internal/rules' own doc comments).
//     Measurement is unaffected throughout: the mount's value keeps
//     arriving regardless of the verdict.
//  3. The operator watches the same item again (not merely deletes the
//     decision — an explicit "watch" row the same way an operator who
//     changed their mind would edit it from the Items tab): the item is
//     judged again, and the host returns to crit.
//
// How the crossing is driven, and why not otherwise: like rules_test.go's
// own cpu scenario, nothing here fakes a value — the agent's real disk
// plugin reports this machine's own, genuine disk.used_pct for "/", the
// one mount every platform this suite runs on has. The item-scoped rule's
// own threshold is set below any possible percentage (an always-true
// trigger, the same belowAnyPercentage lever rules_test.go uses for its
// own warn rule), with for_seconds 0 so the tuple reaches crit on the
// first evaluation that sees it — this gate is not about for_duration,
// rules_test.go already owns that claim.
//
// Org-wide seed rules (internal/rules/seed.go) already cover disk.used_pct
// at warn >= 85% and crit >= 95%, but an item-scoped rule always outranks
// an org-scoped one (rules.Resolve's own precedence), so this scenario's
// own always-true item rule governs the "/" tuple regardless of what the
// seed rules would have concluded from the real percentage. The other
// seed rules (cpu and memory, both requiring 5-15 minutes of sustained
// load) have no way to fire inside this test's own short run, so neither
// ever reaches warn or crit on its own — but this scenario still narrows
// their own seed checks to a short host-bound interval (the same
// itemCheckInterval lever as disk), so cpu.used_pct and mem.used_pct have
// each reported — and so resolved a real "ok" against their own seed
// rules — well before the fleet roll-up is asked to read "ok" for claim 2.
// Without that, the host's roll-up would carry zero judged tuples the
// moment the disk item is ignored (nothing yet to call it "ok" about) and
// read as "no data" rather than "ok" — a real distinction (fleet_handlers.
// go's own State: nil doc comment), just not the one this gate is about.
//
// The seed service check (sshd, seed.go's own "every host Pulsix manages
// runs sshd" assumption) is disabled outright, before the agent ever
// enrols: the container this suite runs in has no systemd, so sshd's own
// first reading is a collection error, and internal/rules' own step 1
// (evaluate.go) turns any errored-but-measured tuple unknown regardless of
// any rule — unknown outranks ok in the roll-up (states.go's Worse), so a
// service check nothing here is about would otherwise push the host's
// roll-up to unknown right under this gate's own feet the moment sshd's
// first tick landed. internal/agent/checks' own disk_test.go and
// service_test.go already cover that failure mode in isolation; this gate
// has no claim that needs it turned on.
package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// itemCheckInterval is the host-bound disk check's own interval: the
// minimum internal/checks.minInterval accepts, the same lever
// ruleCheckInterval uses in rules_test.go, so discovery and every
// evaluation this scenario waits on run in seconds rather than minutes.
const itemCheckInterval = 10 * time.Second

// itemMount is the one mount this scenario asks the real disk plugin to
// report (an explicit "mounts" param, disk.go's own doc comment) — "/",
// the one mount point every platform this suite runs on is guaranteed to
// have, so the scenario never depends on what else happens to be mounted
// on the machine running it.
const itemMount = "/"

// diskUsedPct is the metric the item-scoped rule judges, and the one
// diskTupleState and waitForItemTupleState read back.
const diskUsedPct = "disk.used_pct"

// itemTupleOut is the fields of GET /hosts/{id}/checks' value entries this
// gate needs: the v0.3b-2 rule verdict (State) plus v0.3b-3's own Ignored
// flag — hostCheckValueDTO's addition this milestone makes (host_check_
// handlers.go) — which ruleTupleOut (rules_test.go) predates and so does
// not carry.
type itemTupleOut struct {
	Item    string   `json:"item"`
	Metric  string   `json:"metric"`
	Value   *float64 `json:"value"`
	State   *string  `json:"state"`
	Ignored bool     `json:"ignored"`
}

type itemHostCheckOut struct {
	Key    string         `json:"key"`
	Values []itemTupleOut `json:"values"`
}

// diskTupleState reads the host page's own monitoring section for
// itemMount's disk.used_pct tuple: its current state (nil when never
// evaluated or dropped as ignored), its last reported value, and whether
// an item decision currently keeps it from being judged.
func diskTupleState(t *testing.T, w agentWorld, hostID string) (state *string, value *float64, ignored bool) {
	t.Helper()
	var out struct {
		Checks []itemHostCheckOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/checks", nil, &out)
	for _, c := range out.Checks {
		if c.Key != "disk" {
			continue
		}
		for _, v := range c.Values {
			if v.Item == itemMount && v.Metric == diskUsedPct {
				return v.State, v.Value, v.Ignored
			}
		}
	}
	return nil, nil, false
}

// waitForItemTupleState polls the host page until itemMount's disk.used_pct
// tuple reads the wanted state and ignored flag — the way an operator
// watching the host page would, since nothing pushes either. what names
// the phase being waited for, for a timeout's own failure message.
func waitForItemTupleState(t *testing.T, w agentWorld, hostID string, wantState *string, wantIgnored bool, what string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastState *string
	var lastIgnored bool
	for time.Now().Before(deadline) {
		lastState, _, lastIgnored = diskTupleState(t, w, hostID)
		stateMatches := (lastState == nil && wantState == nil) || (lastState != nil && wantState != nil && *lastState == *wantState)
		if stateMatches && lastIgnored == wantIgnored {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	gotState := "<none>"
	if lastState != nil {
		gotState = *lastState
	}
	wantStateStr := "<none>"
	if wantState != nil {
		wantStateStr = *wantState
	}
	t.Fatalf("timed out waiting for %s (disk.used_pct state %q, ignored %v); last state %s, last ignored %v",
		what, wantStateStr, wantIgnored, gotState, lastIgnored)
}

// fleetHostState reads the fleet overview's roll-up for hostID — the same
// route rules_test.go's own TestRulePipelineGate asserts its final claim
// through, so a host decision's effect on the whole-host colour is read
// the exact way the fleet page reads it, not off any per-tuple field.
func fleetHostState(t *testing.T, w agentWorld, hostID string) *string {
	t.Helper()
	var fleet struct {
		Hosts []struct {
			HostID string  `json:"host_id"`
			State  *string `json:"state"`
		} `json:"hosts"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/fleet", nil, &fleet)
	for _, h := range fleet.Hosts {
		if h.HostID == hostID {
			return h.State
		}
	}
	return nil
}

// waitForFleetState polls the fleet overview until hostID's roll-up reads
// want.
func waitForFleetState(t *testing.T, w agentWorld, hostID, want, what string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last *string
	for time.Now().Before(deadline) {
		last = fleetHostState(t, w, hostID)
		if last != nil && *last == want {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	got := "<none>"
	if last != nil {
		got = *last
	}
	t.Fatalf("timed out waiting for %s (fleet state %q); last fleet state %s", what, want, got)
}

// itemDecisionOut is the create/update response's decision id — the only
// field this gate reads back, so it can PUT the same decision again with a
// different verdict (the "watch it again" act).
type itemDecisionOut struct {
	Decision struct {
		ID string `json:"id"`
	} `json:"decision"`
}

// createItemDecision creates a host-scoped, exact-match decision over
// itemMount and returns its id — the same POST the Items tab's "ignore
// this item" action sends.
func createItemDecision(t *testing.T, w agentWorld, hostID, verdict string) string {
	t.Helper()
	var out itemDecisionOut
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/item-decisions", map[string]any{
		"scope": "host", "host_id": hostID, "check_key": "disk",
		"match_kind": "exact", "pattern": itemMount, "decision": verdict,
	}, &out)
	return out.Decision.ID
}

// updateItemDecision replaces decisionID's verdict in place, the same full
// PUT the Items tab's decision editor sends — the "watch it again" act:
// an explicit row, not merely deleting the ignore.
func updateItemDecision(t *testing.T, w agentWorld, decisionID, hostID, verdict string) {
	t.Helper()
	w.api.do(http.MethodPut, "/api/v1/orgs/"+w.org+"/item-decisions/"+decisionID, map[string]any{
		"scope": "host", "host_id": hostID, "check_key": "disk",
		"match_kind": "exact", "pattern": itemMount, "decision": verdict,
	}, nil)
}

func stringPtr(s string) *string { return &s }

func TestItemDecisionPipelineGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx)

	// The seed service check (sshd) is disabled before the agent ever
	// enrols — see this file's own header comment for why a check this
	// scenario has no claim about would otherwise decide its own outcome.
	svc := definitionByKey(t, w.checkDefinitions(t), "service", "org")
	w.api.do(http.MethodPut, "/api/v1/orgs/"+w.org+"/checks/"+svc.ID, map[string]any{
		"key": "service", "plugin": "service", "interval_seconds": svc.IntervalSeconds,
		"enabled": false, "binding_kind": "org", "params": map[string]any{"units": []string{"sshd"}},
	}, nil)

	// --- Setup: an agent, and short-interval checks of its own — disk,
	//     narrowed to the one mount this scenario follows, and cpu/memory,
	//     narrowed only so each reports (and so resolves its own seed rule
	//     to ok) well inside this gate's own short run; see this file's own
	//     header comment -----------------------------------------------
	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-items"), "e2e-items")
	defer stop()
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/checks", map[string]any{
		"key": "disk", "plugin": "disk", "interval_seconds": int(itemCheckInterval / time.Second),
		"enabled": true, "binding_kind": "host", "host_id": hostID,
		"params": map[string]any{"mounts": []string{itemMount}},
	}, nil)
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/checks", map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": int(itemCheckInterval / time.Second),
		"enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/checks", map[string]any{
		"key": "memory", "plugin": "memory", "interval_seconds": int(itemCheckInterval / time.Second),
		"enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)

	// An item-scoped rule over itemMount, with an always-crossed threshold
	// (the same belowAnyPercentage lever rules_test.go's own warn rule
	// uses) and no ripening: the tuple reaches crit the moment the first
	// evaluation sees a real reading for it. It outranks the organization's
	// own seed disk rules (warn >= 85%, crit >= 95%) regardless of
	// itemMount's real usage, since an item-scoped rule always beats an
	// org-scoped one (rules.Resolve's own precedence) — see this file's
	// own header comment.
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/rules", map[string]any{
		"name": "e2e-disk-crit", "enabled": true, "scope": "item", "host_id": hostID, "item": itemMount,
		"metric": diskUsedPct, "severity": "crit", "mode": "form",
		"form":        map[string]any{"metric": diskUsedPct, "op": ">", "value": belowAnyPercentage},
		"for_seconds": 0,
	}, nil)

	// --- 1. The mount's threshold is crossed; the API shows crit ---------
	waitForItemTupleState(t, w, hostID, stringPtr("crit"), false, "itemMount's always-crossed threshold", 60*time.Second)
	waitForFleetState(t, w, hostID, "crit", "the fleet roll-up reflecting the crit tuple", 60*time.Second)

	// --- 2. An operator ignores the item: the next evaluation leaves the
	//        host ok, and the tuple itself carries no state --------------
	// A host-scoped, exact-match decision — the same POST the Items tab's
	// "ignore" action on the host page sends. Creating it marks the host's
	// evaluation cursor dirty (markDirtyForDecision, item_handlers.go), so
	// it takes effect on the very next tick rather than waiting for the
	// host's own staleness deadline.
	decisionID := createItemDecision(t, w, hostID, "ignore")

	// Ignored, and no state: Apply deletes an ignored tuple's check_states
	// row (internal/rules' own doc comment on Result.Dropped), so a null
	// State here means "being ignored", distinguishable from "never
	// evaluated" only by the Ignored flag this assertion also pins.
	waitForItemTupleState(t, w, hostID, nil, true, "the ignored mount carrying no state", 60*time.Second)
	// The host itself returns to ok: with the only crit-producing tuple
	// dropped from judgement, nothing else in this scenario's own short
	// run can turn the roll-up anything but ok (see this file's own header
	// comment on the seed rules that cannot fire in time).
	waitForFleetState(t, w, hostID, "ok", "the fleet roll-up once the only crit tuple is ignored", 60*time.Second)

	// Measurement never stopped: the mount's own value is still arriving,
	// ignoring only ever withholds the verdict (items.Decide's own
	// contract; hostCheckValueDTO.Ignored's own doc comment).
	if _, value, ignored := diskTupleState(t, w, hostID); value == nil || !ignored {
		t.Fatalf("itemMount's value/ignored = %v/%v after ignoring it, want a real value and ignored=true: measurement must continue", value, ignored)
	}

	// --- 3. The operator watches the item again: it is judged again, and
	//        the host returns to crit ------------------------------------
	// An explicit watch, not a delete of the ignore — the same full PUT
	// the Items tab's decision editor sends when an operator changes their
	// mind, which also marks the host dirty again both before and after
	// the edit (item_handlers.go's updateItemDecision route).
	updateItemDecision(t, w, decisionID, hostID, "watch")

	waitForItemTupleState(t, w, hostID, stringPtr("crit"), false, "the watched mount judged again", 60*time.Second)
	waitForFleetState(t, w, hostID, "crit", "the fleet roll-up once the item is watched again", 60*time.Second)
}
