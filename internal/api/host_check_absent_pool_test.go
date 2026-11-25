package api_test

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
)

// TestHostChecksHoldsAnAbsentPoolsOtherMetrics (backlog 3.74, 3.9b
// ruling): a pool the agent reports as EXPORTED (named in optional_pools)
// or MISSING (named in pools) reports zfs.state and nothing else, so while
// that is what it reads, its last capacity is held: still listed, with
// held_while naming the state it waits on, and never stale however old.
// An imported pool's values carry no held_while. Once the pool reports
// ONLINE again, its capacity is an ordinary value again — stale, here,
// since nothing new arrived. A value of an item taken off the check's list
// is still hidden (backlog 3.28): the disk "elsewhere" below.
func TestHostChecksHoldsAnAbsentPoolsOtherMetrics(t *testing.T) {
	f := newChecksFixture(t, "host-checks-absent-pool")
	created := f.createCheck(t, f.operatorC, map[string]any{
		"key": "zfs", "plugin": "zfs", "interval_seconds": 60, "binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"pools": []any{"tank", "gone"}, "optional_pools": []any{"backup"}},
	})
	defer requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+created.ID, nil)

	now := time.Now().UTC().Truncate(time.Microsecond)
	obs := func(item, metric string, at time.Time, value *float64, label *string) checks.Observation {
		return checks.Observation{HostID: f.web, CheckKey: "zfs", Item: item, Metric: metric, At: at, Value: value, Label: label}
	}
	earlier := now.Add(-10 * time.Minute)
	f.record(t,
		obs("tank", "zfs.state", now, nil, strPtr("ONLINE")),
		obs("tank", "zfs.capacity_pct", now, floatPtr(40), nil),
		obs("backup", "zfs.capacity_pct", earlier, floatPtr(91), nil),
		obs("backup", "zfs.state", now, nil, strPtr("EXPORTED")),
		obs("gone", "zfs.capacity_pct", earlier, floatPtr(12), nil),
		obs("gone", "zfs.state", now, nil, strPtr("MISSING")),
		obs("elsewhere", "zfs.capacity_pct", now, floatPtr(5), nil),
		obs("elsewhere", "zfs.state", now, nil, strPtr("ONLINE")),
	)
	// Each value as "item metric held_while stale".
	listed := func(t *testing.T) []string {
		t.Helper()
		var out []string
		for _, v := range f.hostChecks(t, f.viewerC, f.web).byKey(t, "zfs").Values {
			out = append(out, fmt.Sprintf("%s %s %q %v", v.Item, v.Metric, v.HeldWhile, v.Stale))
		}
		sort.Strings(out)
		return out
	}
	want := []string{
		`backup zfs.capacity_pct "EXPORTED" false`, `backup zfs.state "" false`,
		`gone zfs.capacity_pct "MISSING" false`, `gone zfs.state "" false`,
		`tank zfs.capacity_pct "" false`, `tank zfs.state "" false`,
	}
	if got := listed(t); !equalStrings(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}

	f.record(t, obs("backup", "zfs.state", now.Add(time.Second), nil, strPtr("ONLINE")))
	want[0] = `backup zfs.capacity_pct "" true`
	if got := listed(t); !equalStrings(got, want) {
		t.Fatalf("values after the pool is back = %v, want %v", got, want)
	}

	// A reading taken after the pool's MISSING state proves it reports
	// more than its state: it is an ordinary value, not held.
	f.record(t, obs("gone", "zfs.capacity_pct", now.Add(time.Second), floatPtr(13), nil))
	want[2] = `gone zfs.capacity_pct "" false`
	if got := listed(t); !equalStrings(got, want) {
		t.Fatalf("values after a newer reading = %v, want %v", got, want)
	}
}

// TestEffectiveRulesKeepsAnExportedPoolsOtherMetrics (backlog 3.74, 3.9b
// ruling): an exported pool's capacity is held, not unasked — its stored
// state and alert wait on the rule that judged them — so the effective-rules
// view keeps its winner, like an imported pool's.
func TestEffectiveRulesKeepsAnExportedPoolsOtherMetrics(t *testing.T) {
	f := newRulesFixture(t, "rules-exported-pool")
	checksURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/checks"
	resp := postJSON(t, f.operatorC, checksURL, map[string]any{
		"key": "zfs", "plugin": "zfs", "interval_seconds": 60, "binding_kind": "host", "host_id": f.host.String(),
		"params": map[string]any{"optional_pools": []any{"backup"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create check = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	scoped := db.WithOrgScope(context.Background(), f.orgID)
	now := time.Now()
	if _, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.host, CheckKey: "zfs", Item: "tank", Metric: "zfs.state", At: now, Label: strPtr("ONLINE")},
		{HostID: f.host, CheckKey: "zfs", Item: "tank", Metric: "zfs.capacity_pct", At: now, Value: floatPtr(40)},
		{HostID: f.host, CheckKey: "zfs", Item: "backup", Metric: "zfs.state", At: now, Label: strPtr("EXPORTED")},
		{HostID: f.host, CheckKey: "zfs", Item: "backup", Metric: "zfs.capacity_pct", At: now.Add(-time.Minute), Value: floatPtr(91)},
	}); err != nil {
		t.Fatal(err)
	}
	f.createRule(t, f.operatorC, map[string]any{
		"name": "pool full", "scope": "org", "metric": "zfs.capacity_pct", "severity": "crit", "mode": "form",
		"form": map[string]any{"metric": "zfs.capacity_pct", "op": ">", "value": 90},
	})

	resp = requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.host)+"/effective-rules", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("effective-rules = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Effective []effectiveRuleBody `json:"effective"`
	}
	decodeInto(t, resp, &out)
	capacity := map[string]bool{}
	for _, e := range out.Effective {
		if e.CheckKey == "zfs" && e.Metric == "zfs.capacity_pct" {
			capacity[e.Item] = true
		}
	}
	if !capacity["tank"] || !capacity["backup"] || len(capacity) != 2 {
		t.Fatalf("capacity winners on %v, want tank and backup", capacity)
	}
}
