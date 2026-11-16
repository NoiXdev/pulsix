package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/probes"
)

// vantageFixture is a host checks fixture with a tcp check "site" on web-1
// probed by the manager and the agents of the group of role=db (db-1),
// policy majority, and a tcp check "plain" without vantage groups. The
// manager's latest round is up, db-1's down; a vantage no longer expected
// (cold-1) still has rows, as before the runner's next refresh prunes
// them.
func vantageFixture(t *testing.T, slug string) (checksFixture, time.Time) {
	t.Helper()
	f := newChecksFixture(t, slug)
	group := f.createGroup(t, map[string]any{"name": "probes", "selector": "role=db"})
	f.createCheck(t, f.operatorC, map[string]any{
		"key": "site", "plugin": "tcp", "interval_seconds": 60, "binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"port": 80, "vantage_groups": []string{group.ID}, "fail_policy": "majority"},
	})
	f.createCheck(t, f.operatorC, map[string]any{
		"key": "plain", "plugin": "tcp", "interval_seconds": 60, "binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"port": 22},
	})

	ctx := db.WithOrgScope(context.Background(), f.orgID)
	store := probes.NewSQLStore(f.s.DB)
	if err := store.ReplaceOrgAssignments(ctx, f.orgID, []probes.Assignment{{
		OrgID: f.orgID, AgentHostID: f.dbHost, TargetHostID: f.web, CheckKey: "site",
		Plugin: "tcp", Params: map[string]any{"port": 80}, Interval: time.Minute, Address: "10.0.0.1",
	}}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	round := func(vantage string, up, ms float64) {
		t.Helper()
		if err := store.RecordVantage(ctx, f.orgID, vantage, []checks.Observation{
			{HostID: f.web, CheckKey: "site", Metric: "tcp.up", At: at, Value: floatPtr(up)},
			{HostID: f.web, CheckKey: "site", Metric: "tcp.connect_ms", At: at, Value: floatPtr(ms)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	round(probes.VantageManager, 1, 12)
	round(f.dbHost.String(), 0, 40)
	round(f.cold.String(), 1, 99)
	// The combined verdict, as the runner records it.
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "site", Metric: "tcp.up", At: at, Value: floatPtr(1)},
		checks.Observation{HostID: f.web, CheckKey: "site", Metric: "tcp.connect_ms", At: at, Value: floatPtr(12)},
	)
	return f, at
}

// TestHostChecksReportVantages: a check with vantage groups carries its
// fail policy and its expected vantages in combining order — the manager,
// then the agents by host name — each with its state, latest round and
// that round's values; a vantage no longer expected is left out. A check
// without vantage groups carries neither key.
func TestHostChecksReportVantages(t *testing.T) {
	f, at := vantageFixture(t, "host-check-vantages")

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var raw struct {
		Checks []map[string]json.RawMessage `json:"checks"`
	}
	decodeInto(t, resp, &raw)
	type observation struct {
		Item   string   `json:"item"`
		Metric string   `json:"metric"`
		Value  *float64 `json:"value"`
		Label  *string  `json:"label"`
		Error  *string  `json:"error"`
	}
	type vantage struct {
		Vantage      string        `json:"vantage"`
		Name         string        `json:"name"`
		Kind         string        `json:"kind"`
		Status       string        `json:"status"`
		At           *time.Time    `json:"at"`
		Observations []observation `json:"observations"`
	}
	var site []vantage
	var policy string
	seen := map[string]bool{}
	for _, c := range raw.Checks {
		var key string
		if err := json.Unmarshal(c["key"], &key); err != nil {
			t.Fatal(err)
		}
		seen[key] = true
		if key != "site" {
			for _, field := range []string{"vantages", "fail_policy"} {
				if v, ok := c[field]; ok {
					t.Fatalf("check %s without vantage groups carries %s = %s", key, field, v)
				}
			}
			continue
		}
		if err := json.Unmarshal(c["vantages"], &site); err != nil {
			t.Fatalf("site vantages %s: %v", c["vantages"], err)
		}
		if err := json.Unmarshal(c["fail_policy"], &policy); err != nil {
			t.Fatalf("site fail_policy %s: %v", c["fail_policy"], err)
		}
	}
	if !seen["site"] || !seen["plain"] {
		t.Fatalf("checks = %v, want site and plain", seen)
	}
	if policy != "majority" {
		t.Fatalf("fail_policy = %q, want majority", policy)
	}
	want := []vantage{
		{Vantage: "manager", Name: "", Kind: "manager", Status: "up"},
		{Vantage: f.dbHost.String(), Name: "db-1", Kind: "agent", Status: "down"},
	}
	if len(site) != len(want) {
		t.Fatalf("vantages = %+v, want manager and db-1 only (cold-1 is no longer expected)", site)
	}
	for i, w := range want {
		v := site[i]
		if v.Vantage != w.Vantage || v.Name != w.Name || v.Kind != w.Kind || v.Status != w.Status || v.At == nil || !v.At.Equal(at) {
			t.Fatalf("vantage %d = %+v, want %+v at %s", i, v, w, at)
		}
		if len(v.Observations) != 2 {
			t.Fatalf("vantage %d observations = %+v, want its round", i, v.Observations)
		}
		for _, o := range v.Observations {
			if o.Label != nil || o.Error != nil || o.Value == nil {
				t.Fatalf("vantage %d observation %+v: label and error are left out when empty", i, o)
			}
		}
	}
}

// TestHostMetricsVantage: the graph's series reads one vantage's samples
// when vantage names one, the check's own (combined) otherwise; a
// vantage that is neither "manager" nor a canonical host id is refused
// like any other bad parameter.
func TestHostMetricsVantage(t *testing.T) {
	f, _ := vantageFixture(t, "host-metrics-vantage")
	points := func(vantage string) []float64 {
		t.Helper()
		q := url.Values{"check": {"site"}, "metric": {"tcp.connect_ms"}, "range": {"1h"}}
		if vantage != "" {
			q.Set("vantage", vantage)
		}
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?"+q.Encode(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("metrics vantage %q = %d %s", vantage, resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Points []struct {
				Value float64 `json:"value"`
			} `json:"points"`
		}
		decodeInto(t, resp, &out)
		vals := make([]float64, len(out.Points))
		for i, p := range out.Points {
			vals[i] = p.Value
		}
		return vals
	}
	for _, c := range []struct {
		vantage string
		want    float64
	}{{"", 12}, {"manager", 12}, {f.dbHost.String(), 40}, {f.cold.String(), 99}} {
		if got := points(c.vantage); len(got) != 1 || got[0] != c.want {
			t.Fatalf("vantage %q points = %v, want [%v]", c.vantage, got, c.want)
		}
	}
	// A host id of a host with no samples is an empty series, not an error.
	if got := points("0190a000-0000-7000-8000-00000000000a"); len(got) != 0 {
		t.Fatalf("unknown vantage points = %v, want none", got)
	}
	for _, bad := range []string{"combined", "Manager", strings.ToUpper(f.dbHost.String()), "{" + f.dbHost.String() + "}", " "} {
		q := url.Values{"check": {"site"}, "metric": {"tcp.connect_ms"}, "range": {"1h"}, "vantage": {bad}}
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?"+q.Encode(), nil)
		if text := mustReadBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(text, "vantage") {
			t.Fatalf("vantage %q = %d %s, want 400 naming vantage", bad, resp.StatusCode, text)
		}
	}
}
