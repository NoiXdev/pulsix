// The v0.3b-1 milestone gate: the check pipeline end to end, on top of
// the v0.3a agent scenario in agent_test.go (same manager wiring, same
// real agent binary loop, same operator driving everything through the
// HTTP API the GUI uses).
//
// The four things this gate exists to prove, in one scenario:
//
//  1. A fresh organization already measures: the seed set is there, the
//     enrolled agent receives it, runs it, and its samples reach the
//     operator's own views — the host's current values and the graph
//     behind them.
//  2. An edited definition reaches the running agent: the operator
//     narrows a check's interval and the agent re-times that check
//     without restarting, fast enough that the configuration it started
//     with could not have produced what the gate then observes.
//  3. A monitoring group beats the organization: for a host the group's
//     selector matches, the group's definition is the one the host
//     actually runs, and the manager says where it came from.
//  4. A change nothing pushes still arrives. The group and its definition
//     are created before the host is tagged, so the write reaches no host
//     and the tag that makes it apply pushes nothing — no host or tag
//     handler resolves affected hosts at all. The agent can only learn
//     that definition by re-asserting its configuration version and being
//     answered, which is the self-healing property the whole delivery
//     design rests on.
//
// What this gate does NOT assert, deliberately:
//
//   - The `service` plugin's readings. It needs systemd over D-Bus, which
//     the container this suite runs in does not provide; the check is
//     resolved and pushed like any other, but every assertion about
//     collected values here is about `cpu`, which every platform reports.
//   - The configuration version string itself. It never leaves the agent
//     stream (Heartbeat.config_version against the manager's resolved
//     set) and is not part of any operator-facing API, so the gate
//     asserts the effects the version exists to produce — the agent
//     running an edited definition, and an unpushed change arriving
//     anyway — rather than reaching behind the API for the string.
//     internal/agentapi's own tests pin the comparison; this one pins
//     that it changes what a real agent does. Claim 4 is the half that
//     genuinely needs the echo: break it (send an empty config_version
//     forever) and the manager re-pushes on every heartbeat, which claim
//     4 cannot tell apart from a working echo — but break the RESOLVE
//     side, or the re-assert itself, and claim 4 is what fails.
package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// seedCheckKeys is the "Linux base" set every organization is created
// with (internal/checks.SeedDefinitions).
var seedCheckKeys = []string{"cpu", "memory", "disk", "service"}

// cpuMetric is the whole-host metric the gate follows through the whole
// pipeline: the agent's cpu plugin reports it on every platform, as a
// number, which is what a graph needs.
const cpuMetric = "cpu.used_pct"

// checkDefOut mirrors internal/api's checkDefinitionDTO — the Checks
// page's row.
type checkDefOut struct {
	ID              string `json:"id"`
	Key             string `json:"key"`
	Plugin          string `json:"plugin"`
	IntervalSeconds int    `json:"interval_seconds"`
	Enabled         bool   `json:"enabled"`
	Origin          string `json:"origin"`
	BindingKind     string `json:"binding_kind"`
	GroupID         string `json:"group_id"`
	HostID          string `json:"host_id"`
}

// checkValueOut is one current reading on the host page's monitoring card.
type checkValueOut struct {
	Item   string    `json:"item"`
	Metric string    `json:"metric"`
	Value  *float64  `json:"value"`
	Label  string    `json:"label"`
	Error  string    `json:"error"`
	At     time.Time `json:"at"`
	Stale  bool      `json:"stale"`
}

// hostCheckOut is one entry of the host's effective set: the definition
// that won for this key, where it came from, and what it last reported.
type hostCheckOut struct {
	Key             string          `json:"key"`
	Plugin          string          `json:"plugin"`
	IntervalSeconds int             `json:"interval_seconds"`
	Enabled         bool            `json:"enabled"`
	Source          string          `json:"source"`
	SourceID        string          `json:"source_id"`
	SourceName      string          `json:"source_name"`
	Values          []checkValueOut `json:"values"`
}

// checkDefinitions reads the Checks page's list.
func (w agentWorld) checkDefinitions(t *testing.T) []checkDefOut {
	t.Helper()
	var out struct {
		Checks []checkDefOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/checks", nil, &out)
	return out.Checks
}

// definitionByKey returns the single definition with this key and binding
// kind, failing the scenario if there is not exactly one.
func definitionByKey(t *testing.T, defs []checkDefOut, key, binding string) checkDefOut {
	t.Helper()
	var found []checkDefOut
	for _, d := range defs {
		if d.Key == key && d.BindingKind == binding {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		t.Fatalf("definitions with key %q at %s scope = %d, want 1; all = %+v", key, binding, len(found), defs)
	}
	return found[0]
}

// hostChecks reads the host page's monitoring section: the effective set
// with each check's latest values.
func (w agentWorld) hostChecks(t *testing.T, hostID string) []hostCheckOut {
	t.Helper()
	var out struct {
		HostID    string         `json:"host_id"`
		Monitored bool           `json:"monitored"`
		Checks    []hostCheckOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/checks", nil, &out)
	if !out.Monitored {
		t.Fatalf("host %s reads as not monitored; an enrolled agent's host must be", hostID)
	}
	return out.Checks
}

// hostCheck returns the effective entry for one key.
func hostCheck(t *testing.T, list []hostCheckOut, key string) hostCheckOut {
	t.Helper()
	for _, c := range list {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("the host's effective set carries no check %q; set = %+v", key, list)
	return hostCheckOut{}
}

// cpuReading picks the whole-host cpu metric out of one effective entry.
// A reading that carries a collection error is reported as such: the gate
// wants a real measurement, and "the plugin failed" is a different
// outcome from "no reading yet".
func cpuReading(c hostCheckOut) (checkValueOut, bool) {
	for _, v := range c.Values {
		if v.Item == "" && v.Metric == cpuMetric {
			return v, true
		}
	}
	return checkValueOut{}, false
}

// waitForCPUSamples polls the host page until want distinct cpu readings
// taken after since have been observed, and returns their timestamps in
// the order they arrived. Polling is how the operator sees this too — the
// page has no push channel — and at four polls per second no reading of a
// check running on a ten-second interval can be missed.
//
// since exists because the gate measures cadence twice, across a
// configuration change: a reading the previous interval produced must not
// be counted into the window that is supposed to show the new one. Pass
// the zero time to take whatever is there.
func waitForCPUSamples(t *testing.T, w agentWorld, hostID string, since time.Time, want int, within time.Duration) []time.Time {
	t.Helper()
	deadline := time.Now().Add(within)
	var stamps []time.Time
	for time.Now().Before(deadline) {
		entry := hostCheck(t, w.hostChecks(t, hostID), "cpu")
		if v, ok := cpuReading(entry); ok {
			if v.Error != "" {
				t.Fatalf("the agent reports %s as a collection failure: %s", cpuMetric, v.Error)
			}
			if v.Value == nil {
				t.Fatalf("%s arrived without a value: %+v", cpuMetric, v)
			}
			if *v.Value < 0 || *v.Value > 100 {
				t.Errorf("%s = %v, want a percentage", cpuMetric, *v.Value)
			}
			fresh := v.At.After(since)
			if fresh && (len(stamps) == 0 || !v.At.Equal(stamps[len(stamps)-1])) {
				stamps = append(stamps, v.At)
			}
			if len(stamps) >= want {
				return stamps
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d distinct %s readings after %s; got %v", want, cpuMetric, since, stamps)
	return nil
}

func TestCheckPipelineGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx)

	// --- 1. A fresh organization ships the seed set ----------------------
	defs := w.checkDefinitions(t)
	if len(defs) != len(seedCheckKeys) {
		t.Fatalf("a fresh organization has %d check definitions, want the %d seed rows; got %+v",
			len(defs), len(seedCheckKeys), defs)
	}
	for _, key := range seedCheckKeys {
		d := definitionByKey(t, defs, key, "org")
		if d.Origin != "seed" || !d.Enabled || d.Plugin != key {
			t.Errorf("seed definition %q = %+v, want an enabled seed row for its own plugin", key, d)
		}
	}
	seedCPU := definitionByKey(t, defs, "cpu", "org")
	if seedCPU.IntervalSeconds != 60 {
		t.Fatalf("the seed cpu interval is %ds, want 60 — the rest of this gate is written against it",
			seedCPU.IntervalSeconds)
	}

	// --- 2. The agent enrols and receives the set ------------------------
	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-checks"), "e2e-checks")
	defer stop()
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	// Everything the organization defines applies to this host, bound at
	// the organization's own scope since nothing narrower exists yet.
	effective := w.hostChecks(t, hostID)
	if len(effective) != len(seedCheckKeys) {
		t.Fatalf("the host's effective set has %d checks, want %d; got %+v", len(effective), len(seedCheckKeys), effective)
	}
	for _, key := range seedCheckKeys {
		if got := hostCheck(t, effective, key).Source; got != "org" {
			t.Errorf("check %q resolves to source %q, want org", key, got)
		}
	}

	// --- 3. An edited interval reaches the running agent -----------------
	// The seed cpu check runs once a minute, which is the right default
	// and the wrong thing to wait for: the operator narrows it to the
	// shortest interval Pulsix accepts, and the agent re-times that one
	// check. The wait below is therefore both halves of the claim — the
	// samples flow at all, and they flow at the new cadence.
	w.api.do(http.MethodPut, "/api/v1/orgs/"+w.org+"/checks/"+seedCPU.ID, map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": 10, "enabled": true, "binding_kind": "org",
	}, nil)
	if got := hostCheck(t, w.hostChecks(t, hostID), "cpu").IntervalSeconds; got != 10 {
		t.Fatalf("the host's effective cpu interval is %ds after the edit, want 10", got)
	}

	// Two distinct readings ten seconds apart. The configuration the
	// agent enrolled with — one reading a minute — cannot produce them,
	// whatever the machine's load: this is what "the agent adopted the
	// edit" means in observable terms.
	stamps := waitForCPUSamples(t, w, hostID, time.Time{}, 2, 75*time.Second)
	if gap := stamps[1].Sub(stamps[0]); gap > 30*time.Second {
		t.Errorf("consecutive %s readings are %v apart; the agent is still running the 60s definition", cpuMetric, gap)
	}

	// --- 4. The readings are behind the operator's graph -----------------
	var series struct {
		Check  string    `json:"check"`
		Metric string    `json:"metric"`
		Range  string    `json:"range"`
		From   time.Time `json:"from"`
		To     time.Time `json:"to"`
		Points []struct {
			At    time.Time `json:"at"`
			Value float64   `json:"value"`
		} `json:"points"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+
		"/metrics?check=cpu&metric="+cpuMetric+"&range=1h", nil, &series)
	if len(series.Points) == 0 {
		t.Fatalf("the 1h graph for %s has no points, but the host page shows current readings for it", cpuMetric)
	}
	for _, p := range series.Points {
		if p.At.Before(series.From) || p.At.After(series.To) {
			t.Errorf("graph point at %s falls outside the requested window %s..%s", p.At, series.From, series.To)
		}
		if p.Value < 0 || p.Value > 100 {
			t.Errorf("graph point = %v, want a percentage", p.Value)
		}
	}

	// --- 5. A monitoring group beats the organization, and the agent
	//        learns it without a push ---------------------------------
	// Order matters here, and it is the whole point of this act. The group
	// and its definition are written FIRST, while the selector matches
	// nothing: a write whose bindings reach no host pushes to nobody (see
	// checks.Affected and api.pushAffected). Only then is the host tagged
	// — and no host or tag handler pushes anything at all, by construction:
	// nothing in them calls AffectedHosts.
	//
	// So the definition that reaches the agent at the end of this act
	// reaches it through the one path acts 3 and 5 would otherwise never
	// touch: the agent's own heartbeat re-assert, answered by the manager
	// because the version it resolves no longer matches the one the agent
	// reports. That is the "a push that never landed heals itself"
	// property the design rests on, and it is otherwise untested end to
	// end.
	var group struct {
		Group struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"group"`
	}
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/monitoring-groups",
		map[string]any{"name": "e2e-tagged", "selector": "role=e2e", "priority": 10}, &group)
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/checks", map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": 20, "enabled": true,
		"binding_kind": "group", "group_id": group.Group.ID,
	}, nil)

	// Nothing matches the group yet, so the host still runs the
	// organization's definition. This is what makes the assertion after
	// the tag mean something: the 20s cadence cannot already be running.
	if got := hostCheck(t, w.hostChecks(t, hostID), "cpu"); got.Source != "org" || got.IntervalSeconds != 10 {
		t.Fatalf("before the tag, cpu resolves to %s/%ds; want the organization's 10s — the group matches no host yet",
			got.Source, got.IntervalSeconds)
	}

	taggedAt := time.Now()
	w.api.do(http.MethodPut, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/tags",
		[]map[string]string{{"key": "role", "value": "e2e"}}, nil)

	var preview struct {
		Count int `json:"count"`
		Hosts []struct {
			ID string `json:"id"`
		} `json:"hosts"`
	}
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/monitoring-groups/preview",
		map[string]string{"selector": "role=e2e"}, &preview)
	if preview.Count != 1 || len(preview.Hosts) != 1 || preview.Hosts[0].ID != hostID {
		t.Fatalf("the selector role=e2e resolves to %+v, want the tagged host alone", preview)
	}

	// Manager side: the group's definition wins for this host, and the
	// page names the scope it came from.
	won := hostCheck(t, w.hostChecks(t, hostID), "cpu")
	if won.Source != "group" || won.SourceID != group.Group.ID {
		t.Fatalf("cpu resolves to source %q/%q, want the monitoring group %s", won.Source, won.SourceID, group.Group.ID)
	}
	if won.SourceName != "e2e-tagged" {
		t.Errorf("the winning scope is named %q, want the group's own name", won.SourceName)
	}
	if won.IntervalSeconds != 20 {
		t.Errorf("the effective cpu interval is %ds, want the group's 20", won.IntervalSeconds)
	}

	// Agent side: the readings slow from one every ten seconds to one
	// every twenty. Three timestamps, because the first of them can still
	// be the old timer's last tick and the one straddling the change
	// carries whatever was left of it — the gap between the last two is
	// the settled cadence. Under the definition the agent was running a
	// moment ago that gap would be ten seconds; under the group's it is
	// twenty, and nothing pushed it there.
	healed := waitForCPUSamples(t, w, hostID, taggedAt, 3, 120*time.Second)
	if gap := healed[2].Sub(healed[1]); gap <= 15*time.Second || gap >= 30*time.Second {
		t.Errorf("settled %s cadence after the tag is %v, want the group's 20s (readings %v)", cpuMetric, gap, healed)
	}

	// The organization's own row is untouched: it lost the key for this
	// host, not its existence — a host the group's selector stops
	// matching falls straight back to it.
	after := w.checkDefinitions(t)
	if got := definitionByKey(t, after, "cpu", "org").IntervalSeconds; got != 10 {
		t.Errorf("the organization's cpu definition now reads %ds, want the 10 the operator set", got)
	}
	if got := definitionByKey(t, after, "cpu", "group").GroupID; got != group.Group.ID {
		t.Errorf("the group-bound cpu definition points at group %q, want %q", got, group.Group.ID)
	}
}
