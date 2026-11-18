// DELETE /orgs/{org}/hosts/{id}/items (backlog 3.12b): the host page's
// Forget action. The forgetting itself — every table, the transaction, the
// races — is internal/checks' and its store's to prove; these tests pin the
// HTTP surface: the status codes, the query shape (an item name travels in
// the query, and an empty one is the whole-host item), the role, the audit
// entry, and that the route reaches the alert closer the manager wires.
package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/rules"
)

// forgetURL builds the route's URL with check_key and item URL-encoded.
// withItem false leaves the item parameter out altogether, which is not
// the same request as an empty one.
func forgetURL(hostURL, checkKey, item string, withItem bool) string {
	q := url.Values{}
	q.Set("check_key", checkKey)
	if withItem {
		q.Set("item", item)
	}
	return hostURL + "/items?" + q.Encode()
}

// currentRows counts hostID's check_current rows for (checkKey, item).
func currentRows(t *testing.T, f checksFixture, hostID uuid.UUID, checkKey, item string) int {
	t.Helper()
	var n int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM check_current WHERE host_id = $1 AND check_key = $2 AND item = $3`,
		hostID, checkKey, item).Scan(&n); err != nil {
		t.Fatalf("count current rows: %v", err)
	}
	return n
}

func TestForgetItemRoute(t *testing.T) {
	f := newChecksFixture(t, "forget-item")
	at := time.Now().Add(-10 * time.Minute)
	// An item name with characters a path could not carry, and the
	// whole-host item "".
	odd := "/mnt/a b/?x=1&y#z"
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: odd, Metric: "used_percent", At: at, Value: floatPtr(50)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: odd, Metric: "free_bytes", At: at, Value: floatPtr(1)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: at, Value: floatPtr(10)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: at, Value: floatPtr(5)},
	)
	host := f.hostURL(f.web)

	t.Run("a viewer may not forget", func(t *testing.T) {
		resp := requestJSON(t, f.viewerC, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer forget = %d %s, want 403", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "disk", odd); n != 2 {
			t.Fatalf("current rows after a refused forget = %d, want 2", n)
		}
	})

	t.Run("a non-member reads it as no such organization", func(t *testing.T) {
		resp := requestJSON(t, f.stranger, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("stranger forget = %d, want 404 or 403", resp.StatusCode)
		}
	})

	t.Run("check_key is required", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, host+"/items?item=x", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("forget without check_key = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	// An absent item is refused rather than read as the whole-host item: a
	// client that forgot the parameter must not wipe the host-wide values.
	t.Run("item must be present, even if empty", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "cpu", "", false), nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("forget without item = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "cpu", ""); n != 1 {
			t.Fatalf("whole-host rows after a refused forget = %d, want 1", n)
		}
	})

	t.Run("nothing matched is 404", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "disk", "/never", true), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("forget unknown item = %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	t.Run("a host of no organization the caller sees is 404", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(f.hostURL(uuid.New()), "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("forget on unknown host = %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	// A real host of another organization, with a reading of the same
	// check and item, addressed through this organization's URL: nothing
	// matched here, and the other organization keeps its rows.
	t.Run("another organization's host is 404 and keeps its rows", func(t *testing.T) {
		otherOwner := f.s.SeedUser("forget-item-other-owner@example.test", "correct horse battery")
		other := f.s.SeedOrg("forget-item-other", otherOwner)
		scoped := db.WithOrgScope(context.Background(), other)
		zones, err := f.s.Deps.Hosts.ListZones(scoped, other)
		if err != nil || len(zones) == 0 {
			t.Fatalf("zones: %v", err)
		}
		foreign := seedCheckHost(t, f.s, scoped, other, zones[0].ID, "foreign-1", "10.9.0.1", true, "")
		if res, err := f.s.Checks.RecordObservations(scoped, other, []checks.Observation{
			{HostID: foreign, CheckKey: "disk", Item: odd, Metric: "used_percent", At: at, Value: floatPtr(50)},
		}); err != nil || res.Accepted != 1 {
			t.Fatalf("record on the other organization's host: %+v, %v", res, err)
		}
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(f.hostURL(foreign), "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("forget on another organization's host = %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, foreign, "disk", odd); n != 1 {
			t.Fatalf("the other organization's rows after the refused forget = %d, want 1", n)
		}
	})

	// A repeated parameter is ambiguous — which item was meant? — and is
	// refused rather than resolved to whichever one the decoder keeps.
	t.Run("a repeated item or check_key is 400", func(t *testing.T) {
		for _, q := range []string{
			"check_key=disk&item=" + url.QueryEscape(odd) + "&item=%2F",
			"check_key=disk&check_key=cpu&item=" + url.QueryEscape(odd),
		} {
			resp := requestJSON(t, f.operatorC, http.MethodDelete, host+"/items?"+q, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("forget with %q = %d %s, want 400", q, resp.StatusCode, mustReadBody(t, resp))
			}
		}
		if n := currentRows(t, f, f.web, "disk", odd); n != 2 {
			t.Fatalf("current rows after refused forgets = %d, want 2", n)
		}
		if n := currentRows(t, f, f.web, "disk", "/"); n != 1 {
			t.Fatalf("'/' rows after refused forgets = %d, want 1", n)
		}
	})

	t.Run("an operator forgets an item with any characters in its name", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "disk", odd); n != 0 {
			t.Fatalf("current rows after forget = %d, want 0", n)
		}
		if n := currentRows(t, f, f.web, "disk", "/"); n != 1 {
			t.Fatalf("a sibling item lost its rows: %d, want 1", n)
		}
		// A second forget finds nothing left.
		again := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if again.StatusCode != http.StatusNotFound {
			t.Fatalf("second forget = %d, want 404", again.StatusCode)
		}
	})

	t.Run("item= forgets the whole-host item", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "cpu", "", true), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget whole-host item = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "cpu", ""); n != 0 {
			t.Fatalf("whole-host rows after forget = %d, want 0", n)
		}
	})

	t.Run("each forget is audited with host, check key, item and actor", func(t *testing.T) {
		resp := requestJSON(t, f.ownerC, http.MethodGet, f.activityURL(), nil)
		var found []eventDTO
		for _, ev := range decodeActivity(t, resp).Items {
			if ev.Action == "DELETE forgetHostItem" {
				found = append(found, ev)
			}
		}
		// Two successful forgets; the refused and not-found ones leave none.
		if len(found) != 2 {
			t.Fatalf("forget audit events = %d (%+v), want 2", len(found), found)
		}
		var sawOdd, sawWhole bool
		for _, ev := range found {
			if ev.ObjectID != f.web.String() || ev.ObjectType != "host" || ev.ActorKind != "user" || ev.ActorID == nil {
				t.Fatalf("event %+v does not name the host and a user actor", ev)
			}
			switch {
			case strings.Contains(ev.Summary, `"disk"`) && strings.Contains(ev.Summary, `"/mnt/a b/?x=1&y#z"`):
				sawOdd = true
			case strings.Contains(ev.Summary, `"cpu"`) && strings.Contains(ev.Summary, `""`):
				sawWhole = true
			}
		}
		if !sawOdd || !sawWhole {
			t.Fatalf("summaries %+v do not name each forget's check key and item", found)
		}
	})
}

// TestForgetItemRouteClosesTheItemsAlerts: the route reaches the alert
// closer — the apitest server is wired like the manager, so an open alert
// on the forgotten item comes back closed as forgotten.
func TestForgetItemRouteClosesTheItemsAlerts(t *testing.T) {
	f := newAlertFixture(t, "forget-item-alert")
	at := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if res, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.hostA, CheckKey: "disk", Item: "/data", Metric: "used_percent", At: at, Value: floatPtr(97)},
	}); err != nil || res.Accepted != 1 {
		t.Fatalf("record: %+v, %v", res, err)
	}
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/data", "used_percent"), rules.StateCrit, at)

	hostURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + f.hostA.String()
	resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(hostURL, "disk", "/data", true), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	got, detail := f.getAlert(t, f.viewerC, id)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get alert = %d", got.StatusCode)
	}
	if detail.ClosedAt == nil || detail.ClosedReason != "forgotten" {
		t.Fatalf("closed_at/closed_reason = %v/%q, want set/forgotten", detail.ClosedAt, detail.ClosedReason)
	}
}

// forgetMetricURL is forgetURL with the optional metric parameter
// (backlog 3.88): the metric-level forget of a vanished row.
func forgetMetricURL(hostURL, checkKey, item, metric string) string {
	q := url.Values{}
	q.Set("check_key", checkKey)
	q.Set("item", item)
	q.Set("metric", metric)
	return hostURL + "/items?" + q.Encode()
}

// metricCurrentRows counts hostID's check_current rows of one metric.
func metricCurrentRows(t *testing.T, f checksFixture, hostID uuid.UUID, checkKey, item, metric string) int {
	t.Helper()
	var n int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM check_current WHERE host_id = $1 AND check_key = $2 AND item = $3 AND metric = $4`,
		hostID, checkKey, item, metric).Scan(&n); err != nil {
		t.Fatalf("count metric rows: %v", err)
	}
	return n
}

// TestForgetMetricRoute (backlog 3.88): the item forget route with
// metric= forgets that one metric of the item and nothing else — the same
// role, the same 404 for nothing to forget, an audit entry naming the
// metric. An empty or repeated metric is refused rather than read as the
// whole item.
func TestForgetMetricRoute(t *testing.T) {
	f := newChecksFixture(t, "forget-metric")
	at := time.Now().Add(-10 * time.Minute)
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "http", Metric: "http.dns_ms", At: at.Add(-time.Minute), Value: floatPtr(4)},
		checks.Observation{HostID: f.web, CheckKey: "http", Metric: "http.up", At: at, Value: floatPtr(1)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/a b", Metric: "used_percent", At: at, Value: floatPtr(50)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/a b", Metric: "free_bytes", At: at, Value: floatPtr(1)},
	)
	host := f.hostURL(f.web)

	t.Run("a viewer may not forget a metric", func(t *testing.T) {
		resp := requestJSON(t, f.viewerC, http.MethodDelete, forgetMetricURL(host, "http", "", "http.dns_ms"), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer forget = %d %s, want 403", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	t.Run("an empty or repeated metric is 400 and forgets nothing", func(t *testing.T) {
		for _, q := range []string{
			"check_key=http&item=&metric=",
			"check_key=http&item=&metric=http.dns_ms&metric=http.up",
		} {
			resp := requestJSON(t, f.operatorC, http.MethodDelete, host+"/items?"+q, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("forget with %q = %d %s, want 400", q, resp.StatusCode, mustReadBody(t, resp))
			}
		}
		if n := currentRows(t, f, f.web, "http", ""); n != 2 {
			t.Fatalf("http rows after refused forgets = %d, want 2", n)
		}
	})

	t.Run("a metric the item does not have is 404", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetMetricURL(host, "http", "", "http.tls_ms"), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("forget unknown metric = %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "http", ""); n != 2 {
			t.Fatalf("http rows after a not-found forget = %d, want 2", n)
		}
	})

	t.Run("an operator forgets one metric of the whole-host item", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetMetricURL(host, "http", "", "http.dns_ms"), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget metric = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := metricCurrentRows(t, f, f.web, "http", "", "http.dns_ms"); n != 0 {
			t.Fatalf("forgotten metric keeps %d rows", n)
		}
		if n := metricCurrentRows(t, f, f.web, "http", "", "http.up"); n != 1 {
			t.Fatalf("the item's other metric has %d rows, want 1", n)
		}
		again := requestJSON(t, f.operatorC, http.MethodDelete, forgetMetricURL(host, "http", "", "http.dns_ms"), nil)
		if again.StatusCode != http.StatusNotFound {
			t.Fatalf("second forget = %d, want 404", again.StatusCode)
		}
	})

	t.Run("a named item's metric", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetMetricURL(host, "disk", "/a b", "free_bytes"), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget metric = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := metricCurrentRows(t, f, f.web, "disk", "/a b", "used_percent"); n != 1 {
			t.Fatalf("the item's other metric has %d rows, want 1", n)
		}
	})

	// Backlog 3.88, final review: forgetting the item's last current row
	// would orphan its discovery row, so it is refused with 409 and a
	// message pointing to the item forget; nothing changes.
	t.Run("the item's last value is 409 and forgets nothing", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetMetricURL(host, "disk", "/a b", "used_percent"), nil)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("forget the last value = %d %s, want 409", resp.StatusCode, mustReadBody(t, resp))
		}
		if body := mustReadBody(t, resp); !strings.Contains(body, "forget the item") {
			t.Fatalf("409 body %s does not point to forgetting the item", body)
		}
		if n := metricCurrentRows(t, f, f.web, "disk", "/a b", "used_percent"); n != 1 {
			t.Fatalf("a refused forget removed the row: %d left", n)
		}
	})

	t.Run("each metric forget is audited with its metric", func(t *testing.T) {
		resp := requestJSON(t, f.ownerC, http.MethodGet, f.activityURL(), nil)
		var found []eventDTO
		for _, ev := range decodeActivity(t, resp).Items {
			if ev.Action == "DELETE forgetHostItem" {
				found = append(found, ev)
			}
		}
		if len(found) != 2 {
			t.Fatalf("forget audit events = %d (%+v), want 2", len(found), found)
		}
		var sawDNS, sawFree bool
		for _, ev := range found {
			if ev.ObjectID != f.web.String() || ev.ObjectType != "host" {
				t.Fatalf("event %+v does not name the host", ev)
			}
			switch {
			case strings.Contains(ev.Summary, `"http.dns_ms"`) && strings.Contains(ev.Summary, `"http"`):
				sawDNS = true
			case strings.Contains(ev.Summary, `"free_bytes"`) && strings.Contains(ev.Summary, `"/a b"`):
				sawFree = true
			}
		}
		if !sawDNS || !sawFree {
			t.Fatalf("summaries %+v do not name each forget's metric, item and check key", found)
		}
	})
}

// TestForgetMetricRouteClosesOnlyThatMetricsAlert: the metric forget
// closes the forgotten metric's alert as forgotten and leaves the item's
// other alerts open.
func TestForgetMetricRouteClosesOnlyThatMetricsAlert(t *testing.T) {
	f := newAlertFixture(t, "forget-metric-alert")
	at := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if res, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.hostA, CheckKey: "disk", Item: "/data", Metric: "used_percent", At: at, Value: floatPtr(97)},
		{HostID: f.hostA, CheckKey: "disk", Item: "/data", Metric: "free_bytes", At: at, Value: floatPtr(1)},
	}); err != nil || res.Accepted != 2 {
		t.Fatalf("record: %+v, %v", res, err)
	}
	forgotten := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/data", "free_bytes"), rules.StateCrit, at)
	kept := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/data", "used_percent"), rules.StateCrit, at)

	hostURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + f.hostA.String()
	resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetMetricURL(hostURL, "disk", "/data", "free_bytes"), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget metric = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	if _, detail := f.getAlert(t, f.viewerC, forgotten); detail.ClosedAt == nil || detail.ClosedReason != "forgotten" {
		t.Fatalf("forgotten metric's alert closed_at/reason = %v/%q, want set/forgotten", detail.ClosedAt, detail.ClosedReason)
	}
	if _, detail := f.getAlert(t, f.viewerC, kept); detail.ClosedAt != nil {
		t.Fatalf("the item's other alert was closed (%q)", detail.ClosedReason)
	}
}
