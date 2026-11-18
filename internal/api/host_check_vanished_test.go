package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestHostChecksReportsVanishedValues (backlog 3.88): a value whose item
// kept reporting while it did not — the dns_ms a down probe no longer
// measures — is reported as vanished next to its fresh siblings, decided
// by rules.Vanished itself: every row is checked against both the expected
// answer and the function, which pins this caller to it (rule loading's
// own test, TestLoadUsesTheVanishedRule in internal/rules, pins the
// other). The boundary is strict: three intervals behind is still current,
// measured against the item's newest error-free reading. A fresher error
// (http.status_code here) is never vanished itself and is not that newest:
// an error proves the collection ran, not that the other metrics stopped.
// Nor is http.body_match, which the check stopped asking for with
// body_contains unset: it is not listed at all (checkplugins' MetricAsked
// hook), and its fresher reading has no say either.
func TestHostChecksReportsVanishedValues(t *testing.T) {
	f := newChecksFixture(t, "host-checks-vanished")
	created := f.createCheck(t, f.operatorC, map[string]any{
		"key": "web-http", "plugin": "http", "interval_seconds": 60, "binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"url": "https://{address}/"},
	})
	defer requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+created.ID, nil)

	newest := time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Minute)
	obs := func(metric string, at time.Time, value *float64, errText string) checks.Observation {
		return checks.Observation{HostID: f.web, CheckKey: "web-http", Metric: metric, At: at, Value: value, Err: errText}
	}
	failure := obs("http.failure", newest, nil, "")
	failure.Label = strPtr("connection refused")
	f.record(t,
		obs("http.up", newest.Add(-time.Minute), floatPtr(0), ""),
		failure,
		obs("http.connect_ms", newest.Add(-3*time.Minute+time.Second), floatPtr(4), ""),
		obs("http.ttfb_ms", newest.Add(-3*time.Minute), floatPtr(9), ""),
		obs("http.dns_ms", newest.Add(-3*time.Minute-time.Second), floatPtr(2), ""),
		obs("http.tls_ms", newest.Add(-30*time.Minute), floatPtr(7), ""),
		obs("http.status_code", newest.Add(5*time.Minute), nil, "connection refused"),
		obs("http.body_match", newest.Add(4*time.Minute), floatPtr(1), ""),
	)

	want := map[string]bool{
		"http.up":          false,
		"http.failure":     false,
		"http.connect_ms":  false,
		"http.ttfb_ms":     false,
		"http.dns_ms":      true,
		"http.tls_ms":      true,
		"http.status_code": false,
	}
	web := f.hostChecks(t, f.viewerC, f.web).byKey(t, "web-http")
	if len(web.Values) != len(want) {
		t.Fatalf("values = %+v, want exactly %d (http.body_match not asked without body_contains)", web.Values, len(want))
	}
	for _, v := range web.Values {
		w, ok := want[v.Metric]
		if !ok {
			t.Fatalf("unexpected value %+v", v)
		}
		if v.Vanished != w {
			t.Errorf("%s vanished = %v, want %v", v.Metric, v.Vanished, w)
		}
		if fn := rules.Vanished(v.At, newest, time.Minute); fn != v.Vanished {
			t.Errorf("%s: rules.Vanished = %v but the route says %v", v.Metric, fn, v.Vanished)
		}
	}
}

// TestHostChecksReportsNothingVanishedForADisabledCheck (backlog 3.88): a
// disabled check is not evaluated — rule loading gives every tuple of it
// interval 0 and measures nothing against its item — so the host page
// does not call any of its frozen rows vanished either, even the timing
// metric a probe stopped reporting before the check was switched off.
func TestHostChecksReportsNothingVanishedForADisabledCheck(t *testing.T) {
	f := newChecksFixture(t, "host-checks-vanished-disabled")
	created := f.createCheck(t, f.operatorC, map[string]any{
		"key": "web-http", "plugin": "http", "interval_seconds": 60, "binding_kind": "host", "host_id": f.web.String(),
		"params": map[string]any{"url": "https://{address}/"}, "enabled": false,
	})
	defer requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+created.ID, nil)

	newest := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "web-http", Metric: "http.up", At: newest, Value: floatPtr(0)},
		checks.Observation{HostID: f.web, CheckKey: "web-http", Metric: "http.dns_ms", At: newest.Add(-30 * time.Minute), Value: floatPtr(2)},
	)

	web := f.hostChecks(t, f.viewerC, f.web).byKey(t, "web-http")
	if web.Enabled {
		t.Fatal("the fixture's check is enabled")
	}
	if len(web.Values) != 2 {
		t.Fatalf("values = %+v, want both rows listed", web.Values)
	}
	for _, v := range web.Values {
		if v.Vanished {
			t.Errorf("%s of a disabled check is vanished; the evaluator judges nothing there", v.Metric)
		}
	}
}

// TestEffectiveRulesHidesMetricsTheCheckStoppedAsking (backlog 3.88): the
// effective-rules view applies the same metric predicate as the host page
// and the evaluation, so http.body_match keeps its winner while
// body_contains is set and loses it the moment it is removed.
func TestEffectiveRulesHidesMetricsTheCheckStoppedAsking(t *testing.T) {
	f := newRulesFixture(t, "rules-dropped-metric")
	checksURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/checks"
	body := func(params map[string]any) map[string]any {
		return map[string]any{
			"key": "shop", "plugin": "http", "interval_seconds": 60, "binding_kind": "host", "host_id": f.host.String(),
			"params": params,
		}
	}
	resp := postJSON(t, f.operatorC, checksURL, body(map[string]any{"url": "https://{address}/", "body_contains": "Add to cart"}))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create check = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Check checkDTO `json:"check"`
	}
	decodeInto(t, resp, &created)

	scoped := db.WithOrgScope(context.Background(), f.orgID)
	now := time.Now()
	if _, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.host, CheckKey: "shop", Metric: "http.up", At: now, Value: floatPtr(1)},
		{HostID: f.host, CheckKey: "shop", Metric: "http.body_match", At: now, Value: floatPtr(1)},
	}); err != nil {
		t.Fatal(err)
	}
	f.createRule(t, f.operatorC, map[string]any{
		"name": "body rule", "scope": "org", "metric": "http.body_match", "severity": "crit", "mode": "form",
		"form": map[string]any{"metric": "http.body_match", "op": "<", "value": 1},
	})

	listed := func(t *testing.T) bool {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.host)+"/effective-rules", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("effective-rules = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Effective []effectiveRuleBody `json:"effective"`
		}
		decodeInto(t, resp, &out)
		for _, e := range out.Effective {
			if e.CheckKey == "shop" && e.Metric == "http.body_match" {
				return true
			}
		}
		return false
	}
	if !listed(t) {
		t.Fatal("http.body_match is absent while body_contains is set; the fixture is wrong")
	}

	put := requestJSON(t, f.operatorC, http.MethodPut, checksURL+"/"+created.Check.ID, body(map[string]any{"url": "https://{address}/"}))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("removing body_contains = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	if listed(t) {
		t.Fatal("the effective view still lists http.body_match although the check no longer asks for it")
	}
}
