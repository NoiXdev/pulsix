package api_test

import (
	"context"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
)

// TestHostChecksHidesAnAbsentPoolsOtherMetrics (backlog 3.74): a pool the
// agent reports as EXPORTED (named in optional_pools) or MISSING (named in
// pools) reports zfs.state and nothing else, so while that is what it
// reads, the host page lists its zfs.state alone — its last capacity is
// not asked, like a mount taken off a disk check's list (backlog 3.28) —
// and an imported pool keeps every value. Once the pool reports ONLINE
// again, its capacity is listed again.
func TestHostChecksHidesAnAbsentPoolsOtherMetrics(t *testing.T) {
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
	earlier := now.Add(-2 * time.Minute)
	f.record(t,
		obs("tank", "zfs.state", now, nil, strPtr("ONLINE")),
		obs("tank", "zfs.capacity_pct", now, floatPtr(40), nil),
		obs("backup", "zfs.capacity_pct", earlier, floatPtr(91), nil),
		obs("backup", "zfs.state", now, nil, strPtr("EXPORTED")),
		obs("gone", "zfs.capacity_pct", earlier, floatPtr(12), nil),
		obs("gone", "zfs.state", now, nil, strPtr("MISSING")),
	)
	listed := func(t *testing.T) []string {
		t.Helper()
		var out []string
		for _, v := range f.hostChecks(t, f.viewerC, f.web).byKey(t, "zfs").Values {
			out = append(out, v.Item+" "+v.Metric)
		}
		sort.Strings(out)
		return out
	}
	want := []string{"backup zfs.state", "gone zfs.state", "tank zfs.capacity_pct", "tank zfs.state"}
	if got := listed(t); !equalStrings(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}

	f.record(t, obs("backup", "zfs.state", now.Add(time.Second), nil, strPtr("ONLINE")))
	want = []string{"backup zfs.capacity_pct", "backup zfs.state", "gone zfs.state", "tank zfs.capacity_pct", "tank zfs.state"}
	if got := listed(t); !equalStrings(got, want) {
		t.Fatalf("values after the pool is back = %v, want %v", got, want)
	}
}

// TestEffectiveRulesHidesAnExportedPoolsOtherMetrics (backlog 3.74): the
// effective-rules view reads the item's current state with the same
// predicate as the host page and the evaluation, so an exported pool's
// capacity has no winner while an imported pool's keeps its.
func TestEffectiveRulesHidesAnExportedPoolsOtherMetrics(t *testing.T) {
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
	if !capacity["tank"] || capacity["backup"] || len(capacity) != 1 {
		t.Fatalf("capacity winners on %v, want tank only", capacity)
	}
}
