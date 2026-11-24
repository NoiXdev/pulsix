// Backlog 3.117: the item forget and the metric forget remove the tuple's
// per-vantage probe_current rows with the rows they already delete, so the
// vantage breakdown stops listing a forgotten metric.
package checks_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/probes"
)

func TestForgetsRemoveTheTuplesVantageRows(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-vantage")
	host := seedHost(t, d, org, "forget-vantage-host")
	other := seedHost(t, d, org, "forget-vantage-other")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	scoped := db.WithOrgScope(ctx, org)
	ps := probes.NewSQLStore(d)

	obs := []checks.Observation{
		{HostID: host, CheckKey: "http", Item: "api", Metric: "dns_ms", At: now, Value: f64(3)},
		{HostID: host, CheckKey: "http", Item: "api", Metric: "tls_ms", At: now, Value: f64(4)},
		{HostID: host, CheckKey: "http", Item: "web", Metric: "dns_ms", At: now, Value: f64(5)},
		{HostID: other, CheckKey: "http", Item: "api", Metric: "dns_ms", At: now, Value: f64(6)},
	}
	if _, err := svc.RecordObservations(scoped, org, obs); err != nil {
		t.Fatal(err)
	}
	for _, vantage := range []string{"manager", other.String()} {
		if err := ps.RecordVantage(scoped, org, vantage, obs); err != nil {
			t.Fatal(err)
		}
	}
	loadHost(t, d, org, host)

	has := func(h interface{ String() string }, item, metric string) int {
		t.Helper()
		n := 0
		id := host
		if h.String() == other.String() {
			id = other
		}
		rows, err := ps.VantageCurrent(scoped, org, id, "http")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Item == item && r.Metric == metric {
				n++
			}
		}
		return n
	}
	if has(host, "api", "dns_ms") != 2 {
		t.Fatal("setup: the metric has no vantage rows")
	}

	if err := svc.ForgetMetric(scoped, org, host, "http", "api", "dns_ms"); err != nil {
		t.Fatalf("forget metric: %v", err)
	}
	if n := has(host, "api", "dns_ms"); n != 0 {
		t.Errorf("forgotten metric keeps %d vantage rows", n)
	}
	for name, c := range map[string]struct {
		h            interface{ String() string }
		item, metric string
	}{
		"sibling metric":         {host, "api", "tls_ms"},
		"other item":             {host, "web", "dns_ms"},
		"same tuple, other host": {other, "api", "dns_ms"},
	} {
		if n := has(c.h, c.item, c.metric); n != 2 {
			t.Errorf("%s has %d vantage rows, want 2", name, n)
		}
	}

	if err := svc.ForgetItem(scoped, org, host, "http", "api"); err != nil {
		t.Fatalf("forget item: %v", err)
	}
	if n := has(host, "api", "tls_ms"); n != 0 {
		t.Errorf("forgotten item keeps %d vantage rows", n)
	}
	if n := has(host, "web", "dns_ms"); n != 2 {
		t.Errorf("other item has %d vantage rows, want 2", n)
	}
	if n := has(other, "api", "dns_ms"); n != 2 {
		t.Errorf("same item on another host has %d vantage rows, want 2", n)
	}
}
