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
//     arriving regardless of the verdict. The host still reads ok, not
//     "no data", because cpu.used_pct and mem.used_pct are still judged —
//     the mixed case backlog 3.44 requires stay real: a host with some
//     tuples ignored and some still watched keeps its genuine state.
//  3. The operator watches the same item again (not merely deletes the
//     decision — an explicit "watch" row the same way an operator who
//     changed their mind would edit it from the Items tab): the item is
//     judged again, and the host returns to crit.
//  4. The operator then ignores every tuple this host has — disk's own
//     mount again, plus cpu and memory each through an org-syntax-free,
//     host-scoped glob decision naming no item at all (items.Decide's own
//     doc comment: a decision may name the empty item even though nothing
//     discovers it, since cpu and memory report one whole-host reading
//     with no item to discover — the v0.3b-3 design's own "items with an
//     empty name" ruling). With nothing left to judge, Apply deletes the
//     roll-up row entirely (rules' own "a host with no tuples at all gets
//     no row" contract) — the fleet overview's GET /fleet must now read
//     this host as "all ignored", not as indistinguishable from a host
//     nobody ever measured or one whose agent is broken (backlog 3.44).
//     This is the case the first version of this gate deliberately never
//     reached: claim 2 above keeps cpu and memory judged specifically so
//     the host stays "ok" rather than falling into "no data" for a reason
//     this gate was not about — claim 4 is what now asserts through that
//     exact case instead of routing around it, on the same host, with
//     measurement for every one of its three tuples still proven to
//     continue regardless.
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
// rules — well before the fleet roll-up is asked to read "ok" for claim 2,
// and well before claim 4 asks to ignore them outright.
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

// cpuUsedPct and memUsedPct are the two whole-host metrics claim 4 also
// ignores, through a glob decision naming no item at all — see this file's
// own header comment on why that is a valid target even though neither
// check reports an item to discover.
const (
	cpuUsedPct = "cpu.used_pct"
	memUsedPct = "mem.used_pct"
)

// tupleState reads the host page's own monitoring section for one
// (checkKey, item, metric) tuple: its current state (nil when never
// evaluated or dropped as ignored), its last reported value, and whether
// an item decision currently keeps it from being judged. item is "" for a
// whole-host check like cpu or memory, which report no item at all.
func tupleState(t *testing.T, w agentWorld, hostID, checkKey, item, metric string) (state *string, value *float64, ignored bool) {
	t.Helper()
	var out struct {
		Checks []itemHostCheckOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/checks", nil, &out)
	for _, c := range out.Checks {
		if c.Key != checkKey {
			continue
		}
		for _, v := range c.Values {
			if v.Item == item && v.Metric == metric {
				return v.State, v.Value, v.Ignored
			}
		}
	}
	return nil, nil, false
}

// diskTupleState reads itemMount's disk.used_pct tuple — see tupleState's
// own doc comment for the general shape this specializes.
func diskTupleState(t *testing.T, w agentWorld, hostID string) (state *string, value *float64, ignored bool) {
	t.Helper()
	return tupleState(t, w, hostID, "disk", itemMount, diskUsedPct)
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

// fleetHostRow mirrors the fleet overview's wire shape this gate needs: the
// roll-up state plus backlog 3.44's own all_ignored flag, the field that
// tells apart a host with no roll-up row because nothing ever measured it
// from one with no roll-up row because every item it discovered currently
// resolves to ignored (fleet_handlers.go's own fleetHostDTO.AllIgnored doc
// comment).
type fleetHostRow struct {
	HostID     string  `json:"host_id"`
	State      *string `json:"state"`
	AllIgnored bool    `json:"all_ignored"`
}

// fleetHost reads the fleet overview's row for hostID — the same route
// rules_test.go's own TestRulePipelineGate asserts its final claim through,
// so a host decision's effect on the whole-host colour is read the exact
// way the fleet page reads it, not off any per-tuple field. A host absent
// from the response (should not happen for a host this gate itself
// enrolled) reads as the zero fleetHostRow: state nil, all_ignored false.
func fleetHost(t *testing.T, w agentWorld, hostID string) fleetHostRow {
	t.Helper()
	var fleet struct {
		Hosts []fleetHostRow `json:"hosts"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/fleet", nil, &fleet)
	for _, h := range fleet.Hosts {
		if h.HostID == hostID {
			return h
		}
	}
	return fleetHostRow{}
}

// waitForFleetState polls the fleet overview until hostID's roll-up reads
// want.
func waitForFleetState(t *testing.T, w agentWorld, hostID, want, what string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last *string
	for time.Now().Before(deadline) {
		last = fleetHost(t, w, hostID).State
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

// waitForFleetAllIgnored polls the fleet overview until hostID carries no
// roll-up row (state null) AND all_ignored is set — the distinct reading
// backlog 3.44 gives a host whose every discovered item currently resolves
// to ignored, as opposed to a host nobody has ever measured (state null,
// all_ignored false).
func waitForFleetAllIgnored(t *testing.T, w agentWorld, hostID, what string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last fleetHostRow
	for time.Now().Before(deadline) {
		last = fleetHost(t, w, hostID)
		if last.State == nil && last.AllIgnored {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	got := "<none>"
	if last.State != nil {
		got = *last.State
	}
	t.Fatalf("timed out waiting for %s (fleet state null, all_ignored true); last fleet state %s, last all_ignored %v",
		what, got, last.AllIgnored)
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

// ignoreWholeHostCheck creates a host-scoped, glob decision matching every
// item of checkKey — "*" matches the empty item too (items.Match's own
// doc comment: segments split on '*' both land empty, and an empty prefix
// trivially prefixes, and an empty suffix trivially suffixes, the empty
// remainder) — so this is how claim 4 ignores cpu and memory, neither of
// which has an item for an exact-match decision to name at all.
func ignoreWholeHostCheck(t *testing.T, w agentWorld, hostID, checkKey string) {
	t.Helper()
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/item-decisions", map[string]any{
		"scope": "host", "host_id": hostID, "check_key": checkKey,
		"match_kind": "glob", "pattern": "*", "decision": "ignore",
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

	// --- 4. The operator ignores every tuple this host has: the roll-up
	//        row disappears, and the fleet overview must read that as "all
	//        ignored", not the same "no data" it gives a host nobody has
	//        ever measured (backlog 3.44) ----------------------------------
	// disk's own mount, watched again by claim 3, is ignored a second time
	// by editing decisionID back to "ignore" — not a second, brand-new
	// decision for the same (host, check, pattern): items.Decision.beats'
	// own tie-break between two decisions that otherwise rank equal (same
	// scope, same match kind) favours the OLDER CreatedAt, so a fresh
	// decision would lose outright to claim 3's still-standing "watch" row
	// rather than override it — an operator's own edit, exactly like claim
	// 3's own update, is the correct act here. cpu and memory are ignored
	// for the first time, each through a host-scoped glob naming no item
	// (ignoreWholeHostCheck's own doc comment) — the only way to ignore a
	// whole-host check that discovers no item to pick at all.
	updateItemDecision(t, w, decisionID, hostID, "ignore")
	ignoreWholeHostCheck(t, w, hostID, "cpu")
	ignoreWholeHostCheck(t, w, hostID, "memory")

	// With nothing left for this host to judge, Apply deletes the roll-up
	// row (rules' own "a host with no tuples at all gets no row" contract)
	// — the same absence a host nobody has ever measured leaves. The fleet
	// overview must tell the two apart: this host has three real, discovered
	// tuples and an operator's own decision over every one of them.
	waitForFleetAllIgnored(t, w, hostID, "the fleet roll-up once every tuple is ignored", 60*time.Second)

	// Measurement never stopped for any of the three. disk's own mount
	// carries an item, so GET /hosts/{id}/checks reports its ignored flag
	// directly — the same claim claim 2 already pins. cpu and memory
	// report no item at all, and hostIgnoredItems (host_check_handlers.go)
	// deliberately never looks one up for an itemless metric ("metrics
	// with no item have nothing to ignore", its own doc comment) — that
	// route's ignored flag is an Items-tab affordance, and there is no
	// item row for either of these to show it against. Their own evaluation
	// tuple is still dropped from judgement (resolveIgnored draws no such
	// exception — load_sql.go's own doc comment: "including the empty-item
	// ones"), which is exactly why the fleet roll-up above went to null —
	// so what is left to check here is only that a real value kept
	// arriving for both, not a flag this route was never going to set.
	if _, value, ignored := diskTupleState(t, w, hostID); value == nil || !ignored {
		t.Fatalf("itemMount's value/ignored = %v/%v once all-ignored, want a real value and ignored=true", value, ignored)
	}
	if _, value, _ := tupleState(t, w, hostID, "cpu", "", cpuUsedPct); value == nil {
		t.Fatalf("cpu.used_pct's value = nil once all-ignored, want measurement to continue regardless")
	}
	if _, value, _ := tupleState(t, w, hostID, "memory", "", memUsedPct); value == nil {
		t.Fatalf("mem.used_pct's value = nil once all-ignored, want measurement to continue regardless")
	}
}
