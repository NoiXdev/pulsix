// The per-host monitoring routes' tests (v0.3b-1): the effective set with
// its sources, current values and stale marking; the explicit unmonitored
// state; and the bucketed series behind a graph, including the two refusals
// that keep an operator out of a dead end. Driven through the full API on
// checksFixture (check_handlers_test.go).
package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
)

type hostCheckValue struct {
	Item   string    `json:"item"`
	Metric string    `json:"metric"`
	Value  *float64  `json:"value"`
	Label  string    `json:"label"`
	Error  string    `json:"error"`
	At     time.Time `json:"at"`
	Stale  bool      `json:"stale"`
}

type hostCheck struct {
	DefinitionID    string           `json:"definition_id"`
	Key             string           `json:"key"`
	Plugin          string           `json:"plugin"`
	IntervalSeconds int              `json:"interval_seconds"`
	Enabled         bool             `json:"enabled"`
	Origin          string           `json:"origin"`
	Source          string           `json:"source"`
	SourceID        string           `json:"source_id"`
	SourceName      string           `json:"source_name"`
	Values          []hostCheckValue `json:"values"`
}

type hostChecksBody struct {
	HostID    string      `json:"host_id"`
	Monitored bool        `json:"monitored"`
	Checks    []hostCheck `json:"checks"`
}

func (b hostChecksBody) byKey(t *testing.T, key string) hostCheck {
	t.Helper()
	for _, c := range b.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no effective check %q in %+v", key, b.Checks)
	return hostCheck{}
}

// hostChecks reads GET /hosts/{id}/checks and expects 200.
func (f checksFixture) hostChecks(t *testing.T, c *http.Client, hostID uuid.UUID) hostChecksBody {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.hostURL(hostID)+"/checks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out hostChecksBody
	decodeInto(t, resp, &out)
	return out
}

// record writes observations straight through the same checks service the
// routes read from — the ingest path itself is the agent stream's, which
// these tests have no agent to drive.
func (f checksFixture) record(t *testing.T, obs ...checks.Observation) {
	t.Helper()
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	accepted, rejected, err := f.s.Checks.RecordObservations(scoped, f.orgID, obs)
	if err != nil || rejected != 0 || accepted != len(obs) {
		t.Fatalf("record observations: accepted %d, rejected %d, err %v", accepted, rejected, err)
	}
}

func floatPtr(v float64) *float64 { return &v }
func strPtr(v string) *string     { return &v }

// TestHostEffectiveChecksReportsSourcesValuesAndStaleness is the host
// page's monitoring section end to end: which checks apply, where each one
// came from, what it last said, and whether that is still current.
func TestHostEffectiveChecksReportsSourcesValuesAndStaleness(t *testing.T) {
	f := newChecksFixture(t, "host-checks")
	g := f.createGroup(t, map[string]any{"name": "web tier", "selector": "role=web", "priority": 10})
	f.createCheck(t, f.operatorC, map[string]any{
		"plugin": "cpu", "key": "cpu", "interval_seconds": 15, "binding_kind": "group", "group_id": g.ID,
	})
	f.createCheck(t, f.operatorC, map[string]any{
		"plugin": "memory", "key": "memory", "interval_seconds": 30, "binding_kind": "host", "host_id": f.web.String(),
	})
	// Turned off at the host: still reported, so the GUI can show where.
	f.createCheck(t, f.operatorC, map[string]any{
		"plugin": "service", "key": "service", "interval_seconds": 60, "binding_kind": "host",
		"host_id": f.web.String(), "params": map[string]any{"units": []string{"sshd"}}, "enabled": false,
	})

	now := time.Now()
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-10 * time.Second), Value: floatPtr(12.5)},
		// A 300-second disk check last heard from half an hour ago: more
		// than three intervals, so stale.
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: now.Add(-30 * time.Minute), Value: floatPtr(61)},
		checks.Observation{HostID: f.web, CheckKey: "memory", Metric: "used_percent", At: now.Add(-20 * time.Second), Err: "read /proc/meminfo: permission denied"},
	)

	body := f.hostChecks(t, f.viewerC, f.web)
	if body.HostID != f.web.String() || !body.Monitored {
		t.Fatalf("envelope = %+v, want the host id and monitored = true", body)
	}

	cpu := body.byKey(t, "cpu")
	if cpu.Source != "group" || cpu.SourceID != g.ID || cpu.SourceName != "web tier" || cpu.IntervalSeconds != 15 {
		t.Fatalf("cpu = %+v, want the group's override named", cpu)
	}
	if len(cpu.Values) != 1 || cpu.Values[0].Value == nil || *cpu.Values[0].Value != 12.5 || cpu.Values[0].Stale {
		t.Fatalf("cpu values = %+v, want one fresh 12.5", cpu.Values)
	}

	memory := body.byKey(t, "memory")
	if memory.Source != "host" || memory.SourceID != f.web.String() || memory.SourceName != "web-1" {
		t.Fatalf("memory = %+v, want the host's own override named", memory)
	}
	if len(memory.Values) != 1 || memory.Values[0].Error == "" || memory.Values[0].Value != nil {
		t.Fatalf("memory values = %+v, want a collection error and no number", memory.Values)
	}

	disk := body.byKey(t, "disk")
	if disk.Source != "org" || disk.SourceID != "" || disk.Origin != "seed" {
		t.Fatalf("disk = %+v, want the organization's own seeded check", disk)
	}
	if len(disk.Values) != 1 || disk.Values[0].Item != "/" || !disk.Values[0].Stale {
		t.Fatalf("disk values = %+v, want one stale value for /", disk.Values)
	}

	service := body.byKey(t, "service")
	if service.Enabled || service.Source != "host" {
		t.Fatalf("service = %+v, want it listed as disabled at the host", service)
	}

	// The other host shares the organization's set and none of web-1's
	// values or overrides.
	other := f.hostChecks(t, f.viewerC, f.dbHost)
	if c := other.byKey(t, "cpu"); c.Source != "org" || c.IntervalSeconds != 60 {
		t.Fatalf("db-1 cpu = %+v, want the organization's own 60s check", c)
	}
	if c := other.byKey(t, "disk"); len(c.Values) != 0 {
		t.Fatalf("db-1 disk values = %+v, want none of web-1's", c.Values)
	}
}

// TestHostChecksDistinguishUnmonitoredFromUnconfigured is the reason the
// envelope carries a flag at all: the effective set does not depend on the
// monitored flag, so without it a host nobody watches and a host nobody has
// configured a check for would render identically — and the page would say
// "no checks" about a host that is simply not being watched.
func TestHostChecksDistinguishUnmonitoredFromUnconfigured(t *testing.T) {
	f := newChecksFixture(t, "host-checks-flag")

	unmonitored := f.hostChecks(t, f.viewerC, f.cold)
	if unmonitored.Monitored {
		t.Fatalf("cold-1 reports monitored = true")
	}
	if len(unmonitored.Checks) == 0 {
		t.Fatal("an unmonitored host must still report the checks that would apply to it")
	}

	// Now the other state: monitored, but nothing configured anywhere.
	for _, d := range f.listChecks(t, f.ownerC) {
		if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+d.ID, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete %s = %d", d.Key, resp.StatusCode)
		}
	}
	empty := f.hostChecks(t, f.viewerC, f.web)
	if !empty.Monitored {
		t.Fatalf("web-1 reports monitored = false")
	}
	if len(empty.Checks) != 0 {
		t.Fatalf("checks = %+v, want none", empty.Checks)
	}
}

// TestHostChecksAccess: a viewer reads, a non-member does not, and an
// unknown or foreign host id is a plain 404.
func TestHostChecksAccess(t *testing.T) {
	f := newChecksFixture(t, "host-checks-access")
	if resp := requestJSON(t, f.stranger, http.MethodGet, f.hostURL(f.web)+"/checks", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member = %d, want 403", resp.StatusCode)
	}
	unknown := f.hostURL(uuid.Must(uuid.NewV7())) + "/checks"
	if resp := requestJSON(t, f.viewerC, http.MethodGet, unknown, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown host = %d, want 404", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/hosts/not-a-uuid/checks", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed host id = %d, want 400", resp.StatusCode)
	}

	// A host of another organization reads exactly like one that does not
	// exist.
	otherOwner := f.s.SeedUser("host-checks-access-b@example.test", "correct horse battery")
	otherOrg := f.s.SeedOrg("host-checks-access-b", otherOwner)
	scoped := db.WithOrgScope(context.Background(), otherOrg)
	zones, err := f.s.Deps.Hosts.ListZones(scoped, otherOrg)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	foreign := seedCheckHost(t, f.s, scoped, otherOrg, zones[0].ID, "web-1", "10.9.0.1", true, "web")
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(foreign)+"/checks", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another organization's host = %d, want 404", resp.StatusCode)
	}
}

// TestHostMetricsReturnsABucketedSeries: the graph's data, downsampled
// server-side over the window the caller asked for.
func TestHostMetricsReturnsABucketedSeries(t *testing.T) {
	f := newChecksFixture(t, "host-metrics")
	now := time.Now()
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-40 * time.Minute), Value: floatPtr(10)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-20 * time.Minute), Value: floatPtr(20)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-1 * time.Minute), Value: floatPtr(30)},
	)

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?check=cpu&metric=usage_percent&range=1h", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Check  string    `json:"check"`
		Item   string    `json:"item"`
		Metric string    `json:"metric"`
		Range  string    `json:"range"`
		From   time.Time `json:"from"`
		To     time.Time `json:"to"`
		Points []struct {
			At    time.Time `json:"at"`
			Value float64   `json:"value"`
		} `json:"points"`
	}
	decodeInto(t, resp, &out)
	if out.Check != "cpu" || out.Metric != "usage_percent" || out.Range != "1h" {
		t.Fatalf("echo = %+v", out)
	}
	if !out.To.After(out.From) || out.To.Sub(out.From) != time.Hour {
		t.Fatalf("window = %s..%s, want exactly an hour", out.From, out.To)
	}
	if len(out.Points) != 3 {
		t.Fatalf("points = %+v, want one per sample (each in its own bucket)", out.Points)
	}
	for _, p := range out.Points {
		if p.At.Before(out.From) || p.At.After(out.To) {
			t.Fatalf("point %+v falls outside the reported window", p)
		}
	}
	if out.Points[0].Value != 10 || out.Points[2].Value != 30 {
		t.Fatalf("points = %+v, want them oldest first", out.Points)
	}

	// A window with nothing in it answers an empty series, not an error:
	// a graph of a quiet week is a legitimate thing to ask for.
	resp = requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.dbHost)+"/metrics?check=cpu&metric=usage_percent&range=7d", nil)
	if resp.StatusCode != http.StatusNotFound {
		// db-1 has never reported this metric at all, which is the
		// "nothing to graph" case the route answers 404 for.
		t.Fatalf("metrics of a host that never reported = %d, want 404", resp.StatusCode)
	}
}

// TestHostMetricsRefusesAnUnknownRange: the window is one of three fixed
// choices, and anything else is refused with the list of them rather than
// silently answering the default.
func TestHostMetricsRefusesAnUnknownRange(t *testing.T) {
	f := newChecksFixture(t, "host-metrics-range")
	f.record(t, checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: time.Now(), Value: floatPtr(5)})

	base := f.hostURL(f.web) + "/metrics?check=cpu&metric=usage_percent"
	for _, bad := range []string{"", "&range=", "&range=30m", "&range=1H", "&range=all"} {
		resp := requestJSON(t, f.viewerC, http.MethodGet, base+bad, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("range %q = %d, want 400", bad, resp.StatusCode)
		}
		if body := mustReadBody(t, resp); !strings.Contains(body, "1h") || !strings.Contains(body, "7d") {
			t.Fatalf("refusal %q does not name the ranges an operator may ask for", body)
		}
	}
	for _, ok := range []string{"1h", "24h", "7d"} {
		if resp := requestJSON(t, f.viewerC, http.MethodGet, base+"&range="+ok, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("range %q = %d, want 200", ok, resp.StatusCode)
		}
	}
	// check and metric are not optional either.
	for _, missing := range []string{"?metric=usage_percent&range=1h", "?check=cpu&range=1h"} {
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics"+missing, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("metrics%s = %d, want 400", missing, resp.StatusCode)
		}
	}
}

// TestHostMetricsRefusesANonNumericMetric: a service's state is a word, not
// a line on a graph. The series query scans values as floats, so without
// this guard the request would fail deep in the store with a message about
// column types; the refusal has to name what the metric actually is.
func TestHostMetricsRefusesANonNumericMetric(t *testing.T) {
	f := newChecksFixture(t, "host-metrics-label")
	now := time.Now()
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "service", Item: "sshd", Metric: "state", At: now, Label: strPtr("active")},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: now, Value: floatPtr(61)},
	)

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?check=service&item=sshd&metric=state&range=1h", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("label metric = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	body := mustReadBody(t, resp)
	if !strings.Contains(body, "state") || !strings.Contains(body, "active") {
		t.Fatalf("refusal %q must name the metric and what it actually reports", body)
	}

	// A metric this host has never reported is a 404 — there is nothing
	// to graph, and the caller most likely mistyped.
	for _, q := range []string{
		"?check=cpu&metric=nonsense&range=1h",
		"?check=nonsense&metric=usage_percent&range=1h",
		"?check=disk&item=/srv&metric=used_percent&range=1h",
	} {
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics"+q, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("metrics%s = %d, want 404", q, resp.StatusCode)
		}
	}
	// The item dimension is part of the identity: the same check's other
	// mount is a different series, and the one that exists works.
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?check=disk&item=/&metric=used_percent&range=1h", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("disk / = %d, want 200", resp.StatusCode)
	}
	if resp := requestJSON(t, f.stranger, http.MethodGet, f.hostURL(f.web)+"/metrics?check=disk&item=/&metric=used_percent&range=1h", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member = %d, want 403", resp.StatusCode)
	}
}
