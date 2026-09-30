// The per-host monitoring routes' tests (v0.3b-1): the effective set with
// its sources, current values and stale marking; the explicit unmonitored
// state; and the bucketed series behind a graph, including the two refusals
// that keep an operator out of a dead end. Driven through the full API on
// checksFixture (check_handlers_test.go), whose doc comment explains why
// these subtests share one fixture.
package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

type hostCheckValue struct {
	Item   string    `json:"item"`
	Metric string    `json:"metric"`
	Value  *float64  `json:"value"`
	Label  string    `json:"label"`
	Error  string    `json:"error"`
	At     time.Time `json:"at"`
	Stale  bool      `json:"stale"`
	// State, Since and Flapping are the rule engine's verdict on this tuple
	// (v0.3b-2 Task 11) — additive to the v0.3b-1 fields above.
	State    *string    `json:"state"`
	Since    *time.Time `json:"since"`
	Flapping bool       `json:"flapping"`
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
	HostID             string      `json:"host_id"`
	Monitored          bool        `json:"monitored"`
	Checks             []hostCheck `json:"checks"`
	ClockOffsetSeconds *float64    `json:"clock_offset_seconds"`
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
	res, err := f.s.Checks.RecordObservations(scoped, f.orgID, obs)
	if err != nil || res.Rejected() != 0 || res.Accepted != len(obs) {
		t.Fatalf("record observations: %+v, err %v", res, err)
	}
}

func floatPtr(v float64) *float64 { return &v }
func strPtr(v string) *string     { return &v }

// TestHostChecksReportsTheAgentsClockOffset: staleness alone is not
// actionable. Ingest accepts a reading up to an hour old (replay), the
// page calls one stale after three of the check's own intervals, and a
// host whose clock is four minutes behind therefore reports punctually
// and is badged amber for ever. The envelope carries what the ingest path
// measured so the GUI can name the cause — a clock that disagrees, not a
// host that went quiet.
func TestHostChecksReportsTheAgentsClockOffset(t *testing.T) {
	f := newChecksFixture(t, "host-checks-clock")

	// web-1 reports with a clock four minutes behind the manager's: well
	// inside the plausibility window, so every reading is accepted and
	// stored, and every one of them is three intervals old on arrival.
	f.record(t, checks.Observation{
		HostID: f.web, CheckKey: "cpu", Metric: "usage_percent",
		At: time.Now().Add(-4 * time.Minute), Value: floatPtr(12.5),
	})

	body := f.hostChecks(t, f.viewerC, f.web)
	if body.ClockOffsetSeconds == nil {
		t.Fatal("no clock offset reported for a host that just ingested a batch")
	}
	if *body.ClockOffsetSeconds < 235 || *body.ClockOffsetSeconds > 245 {
		t.Fatalf("clock offset = %v s, want about 240 (positive: the host's clock is behind)", *body.ClockOffsetSeconds)
	}
	// And the reading it measured that on is exactly the one the page
	// would otherwise call stale with no explanation.
	if cpu := body.byKey(t, "cpu"); len(cpu.Values) != 1 || !cpu.Values[0].Stale {
		t.Fatalf("cpu = %+v, want the single reading marked stale", cpu)
	}

	// A host that has reported nothing to this manager gets no offset at
	// all rather than a zero, which would read as "this clock is fine".
	if other := f.hostChecks(t, f.viewerC, f.dbHost); other.ClockOffsetSeconds != nil {
		t.Fatalf("db-1 clock offset = %v, want none: nothing has ever measured it", *other.ClockOffsetSeconds)
	}
}

// TestHostCheckRoutes drives the host page's monitoring section on one
// fixture. The subtests run in order; the last one deletes every
// definition in the organization, so it deliberately comes last.
func TestHostCheckRoutes(t *testing.T) {
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
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-40 * time.Minute), Value: floatPtr(10)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-20 * time.Minute), Value: floatPtr(20)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now.Add(-10 * time.Second), Value: floatPtr(12.5)},
		// The staleness boundary, pinned from both sides against
		// staleIntervals = 3 rather than "old" versus "recent":
		//   - disk runs every 300 s and was last heard from 16 minutes
		//     ago — 3.2 intervals, so stale, and it would NOT be stale at
		//     a threshold of four;
		//   - memory runs every 30 s and was last heard from 65 seconds
		//     ago — 2.2 intervals, so still current, and it WOULD be
		//     marked stale at a threshold of one or two.
		// Together those bracket the constant exactly.
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: now.Add(-16 * time.Minute), Value: floatPtr(61)},
		checks.Observation{HostID: f.web, CheckKey: "memory", Metric: "used_percent", At: now.Add(-65 * time.Second), Err: "read /proc/meminfo: permission denied"},
		checks.Observation{HostID: f.web, CheckKey: "service", Item: "sshd", Metric: "state", At: now, Label: strPtr("active")},
	)

	// Which checks apply, where each one came from, what it last said, and
	// whether that is still current.
	t.Run("the effective set reports sources, values and staleness", func(t *testing.T) {
		body := f.hostChecks(t, f.viewerC, f.web)
		if body.HostID != f.web.String() || !body.Monitored {
			t.Fatalf("envelope = %+v, want the host id and monitored = true", body)
		}

		cpu := body.byKey(t, "cpu")
		if cpu.Source != "group" || cpu.SourceID != g.ID || cpu.SourceName != "web tier" || cpu.IntervalSeconds != 15 {
			t.Fatalf("cpu = %+v, want the group's override named", cpu)
		}
		if len(cpu.Values) != 1 || cpu.Values[0].Value == nil || *cpu.Values[0].Value != 12.5 || cpu.Values[0].Stale {
			t.Fatalf("cpu values = %+v, want one fresh 12.5 (the latest reading only)", cpu.Values)
		}

		memory := body.byKey(t, "memory")
		if memory.Source != "host" || memory.SourceID != f.web.String() || memory.SourceName != "web-1" {
			t.Fatalf("memory = %+v, want the host's own override named", memory)
		}
		if len(memory.Values) != 1 || memory.Values[0].Error == "" || memory.Values[0].Value != nil {
			t.Fatalf("memory values = %+v, want a collection error and no number", memory.Values)
		}
		if memory.IntervalSeconds != 30 || memory.Values[0].Stale {
			t.Fatalf("memory = %+v: a reading 2.2 of its own intervals old is still current", memory)
		}

		disk := body.byKey(t, "disk")
		if disk.Source != "org" || disk.SourceID != "" || disk.Origin != "seed" {
			t.Fatalf("disk = %+v, want the organization's own seeded check", disk)
		}
		if disk.IntervalSeconds != 300 || len(disk.Values) != 1 || disk.Values[0].Item != "/" || !disk.Values[0].Stale {
			t.Fatalf("disk = %+v: a reading 3.2 of its own intervals old is stale", disk)
		}

		service := body.byKey(t, "service")
		if service.Enabled || service.Source != "host" {
			t.Fatalf("service = %+v, want it listed as disabled at the host", service)
		}
		if len(service.Values) != 1 || service.Values[0].Label != "active" || service.Values[0].Value != nil {
			t.Fatalf("service values = %+v, want the discrete state", service.Values)
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
	})

	// A viewer reads, a non-member does not, and an unknown or foreign
	// host id is a plain 404.
	t.Run("access", func(t *testing.T) {
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

		// A host of another organization reads exactly like one that does
		// not exist.
		otherOwner := f.s.SeedUser("host-checks-other@example.test", "correct horse battery")
		otherOrg := f.s.SeedOrg("host-checks-other", otherOwner)
		scoped := db.WithOrgScope(context.Background(), otherOrg)
		zones, err := f.s.Deps.Hosts.ListZones(scoped, otherOrg)
		if err != nil || len(zones) == 0 {
			t.Fatalf("zones: %v", err)
		}
		foreign := seedCheckHost(t, f.s, scoped, otherOrg, zones[0].ID, "web-1", "10.9.0.1", true, "web")
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(foreign)+"/checks", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("another organization's host = %d, want 404", resp.StatusCode)
		}
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(foreign)+"/metrics?check=cpu&metric=usage_percent&range=1h", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("another organization's host metrics = %d, want 404", resp.StatusCode)
		}
	})

	// The graph's data, downsampled server-side over the window the caller
	// asked for.
	t.Run("metrics answer a bucketed series", func(t *testing.T) {
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
		if out.Points[0].Value != 10 || out.Points[2].Value != 12.5 {
			t.Fatalf("points = %+v, want them oldest first", out.Points)
		}
		if resp := requestJSON(t, f.stranger, http.MethodGet, f.hostURL(f.web)+"/metrics?check=cpu&metric=usage_percent&range=1h", nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-member = %d, want 403", resp.StatusCode)
		}
	})

	// The window is one of three fixed choices, and anything else is
	// refused with the list of them rather than silently answered with a
	// default.
	t.Run("metrics refuse an unknown range", func(t *testing.T) {
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
	})

	// A service's state is a word, not a line on a graph. The series query
	// scans values as floats, so without this guard the request would fail
	// deep in the store with a message about column types; the refusal has
	// to name what the metric actually is.
	t.Run("metrics refuse a non-numeric metric", func(t *testing.T) {
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?check=service&item=sshd&metric=state&range=1h", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("label metric = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
		}
		if body := mustReadBody(t, resp); !strings.Contains(body, "state") || !strings.Contains(body, "active") {
			t.Fatalf("refusal %q must name the metric and what it actually reports", body)
		}

		// A metric this host has never reported is a 404 — there is
		// nothing to graph, and the caller most likely mistyped.
		for _, q := range []string{
			"?check=cpu&metric=nonsense&range=1h",
			"?check=nonsense&metric=usage_percent&range=1h",
			"?check=disk&item=/srv&metric=used_percent&range=1h",
		} {
			if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics"+q, nil); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("metrics%s = %d, want 404", q, resp.StatusCode)
			}
		}
		// The item dimension is part of the identity: the same check's
		// other mount is a different series, and the one that exists works.
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/metrics?check=disk&item=/&metric=used_percent&range=1h", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("disk / = %d, want 200", resp.StatusCode)
		}
		// A host that has never reported the metric at all is the same
		// 404, whatever window is asked for.
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.dbHost)+"/metrics?check=cpu&metric=usage_percent&range=7d", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("metrics of a host that never reported = %d, want 404", resp.StatusCode)
		}
	})

	// The reason the envelope carries a flag at all: the effective set
	// does not depend on the monitored flag, so without it a host nobody
	// watches and a host nobody has configured a check for would render
	// identically — and the page would say "no checks" about a host that
	// is simply not being watched.
	//
	// Last: it deletes every definition in the organization.
	t.Run("an unmonitored host is distinguishable from an unconfigured one", func(t *testing.T) {
		unmonitored := f.hostChecks(t, f.viewerC, f.cold)
		if unmonitored.Monitored {
			t.Fatal("cold-1 reports monitored = true")
		}
		if len(unmonitored.Checks) == 0 {
			t.Fatal("an unmonitored host must still report the checks that would apply to it")
		}

		for _, d := range f.listChecks(t, f.ownerC) {
			if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.checksURL()+"/"+d.ID, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("delete %s = %d", d.Key, resp.StatusCode)
			}
		}
		empty := f.hostChecks(t, f.viewerC, f.web)
		if !empty.Monitored {
			t.Fatal("web-1 reports monitored = false")
		}
		if len(empty.Checks) != 0 {
			t.Fatalf("checks = %+v, want none", empty.Checks)
		}
	})
}

// TestHostMetricsAfterACollectionErrorIsAnEmptySeries is the backlog row
// 3.29 that this change closes, reproduced rather than reasoned about: two ordinary ingests on the
// SEEDED service check — one state reading, then one collection failure —
// and an authenticated viewer asking for that metric's graph got
//
//	500 Internal Server Error
//	err="series for host: can't scan into dest[1] (col: value):
//	     cannot scan NULL into *float64"
//
// Two independent defects line up to produce it. check_current's row for
// the tuple is now an ERROR row, carrying neither value nor label, and
// requireNumericMetric only ever tested c.Label != nil — so a row that is
// not a label fell through as "numeric, go ahead". The store's series
// query then averaged value over sample rows that are all labels, and
// avg() of nothing but NULLs is NULL, which the generated scan refuses.
//
// The route must answer an honest empty series. Nothing numeric was ever
// recorded for this tuple, but it HAS reported, so 404 would be a lie and
// 400 would contradict the graph affordance the host page keeps across a
// collection failure (a numeric check whose latest reading is an error
// still has history worth drawing).
func TestHostMetricsAfterACollectionErrorIsAnEmptySeries(t *testing.T) {
	f := newChecksFixture(t, "host-metrics-error-row")
	now := time.Now()

	// Exactly what a real agent sends when sshd cannot be read after
	// having been readable: a label, then an error for the same tuple.
	f.record(t, checks.Observation{
		HostID: f.web, CheckKey: "service", Item: "sshd", Metric: "state",
		At: now.Add(-2 * time.Minute), Label: strPtr("active"),
	})
	f.record(t, checks.Observation{
		HostID: f.web, CheckKey: "service", Item: "sshd", Metric: "state",
		At: now.Add(-time.Minute), Err: "dbus: connection refused",
	})

	// The current row really is the error shape this test exists for —
	// otherwise the request below would be answered by the label branch
	// and prove nothing.
	values := f.hostChecks(t, f.viewerC, f.web).byKey(t, "service").Values
	if len(values) != 1 || values[0].Error == "" || values[0].Label != "" || values[0].Value != nil {
		t.Fatalf("service values = %+v, want exactly one error-only current row", values)
	}

	resp := requestJSON(t, f.viewerC, http.MethodGet,
		f.hostURL(f.web)+"/metrics?check=service&item=sshd&metric=state&range=1h", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics over an error-only current row = %d %s, want 200 with no points",
			resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Check  string `json:"check"`
		Item   string `json:"item"`
		Metric string `json:"metric"`
		Points []struct {
			At    time.Time `json:"at"`
			Value float64   `json:"value"`
		} `json:"points"`
	}
	decodeInto(t, resp, &out)
	if out.Check != "service" || out.Item != "sshd" || out.Metric != "state" {
		t.Fatalf("echo = %+v", out)
	}
	if len(out.Points) != 0 {
		t.Fatalf("points = %+v, want none: every stored sample for this tuple is a label", out.Points)
	}

	// The same route on a numeric metric whose latest reading is an error
	// keeps its history — the case the host page's graph affordance rests
	// on, and the one an over-eager guard would break instead.
	f.record(t, checks.Observation{
		HostID: f.web, CheckKey: "cpu", Metric: "usage_percent",
		At: now.Add(-3 * time.Minute), Value: floatPtr(41),
	})
	f.record(t, checks.Observation{
		HostID: f.web, CheckKey: "cpu", Metric: "usage_percent",
		At: now.Add(-time.Minute), Err: "gopsutil: /proc unreadable",
	})
	resp = requestJSON(t, f.viewerC, http.MethodGet,
		f.hostURL(f.web)+"/metrics?check=cpu&metric=usage_percent&range=1h", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("numeric metric after a collection error = %d %s, want 200",
			resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &out)
	if len(out.Points) != 1 || out.Points[0].Value != 41 {
		t.Fatalf("points = %+v, want the one numeric sample recorded before the failure", out.Points)
	}
}

// TestHostChecksDropsItemsTheDefinitionStoppedAsking: narrowing a disk
// check's mounts must not leave the dropped mount on the host page for
// ever, eventually badged stale — that reads as "the agent stopped
// reporting this" when the truth is "you stopped asking for it". Nothing
// deletes a check_current row (no event says a mount is gone), so the
// filter is on the read side, and the samples behind the dropped item
// stay readable (backlog 3.28).
func TestHostChecksDropsItemsTheDefinitionStoppedAsking(t *testing.T) {
	f := newChecksFixture(t, "host-checks-dropped-item")
	now := time.Now()

	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: now, Value: floatPtr(41)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/var", Metric: "used_percent", At: now, Value: floatPtr(88)},
	)

	// The seeded disk check carries no mounts list, which means every
	// filesystem the host reports — so both are current values, and the
	// assertion below is not passing for want of data.
	if got := itemsOf(f.hostChecks(t, f.viewerC, f.web).byKey(t, "disk")); !equalStrings(got, []string{"/", "/var"}) {
		t.Fatalf("items before the edit = %v, want both mounts", got)
	}

	var seeded checkDTO
	for _, d := range f.listChecks(t, f.ownerC) {
		if d.Key == "disk" {
			seeded = d
		}
	}
	resp := requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+seeded.ID, map[string]any{
		"plugin": "disk", "key": "disk", "interval_seconds": 300, "binding_kind": "org",
		"params": map[string]any{"mounts": []string{"/"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("narrowing the mounts = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	if got := itemsOf(f.hostChecks(t, f.viewerC, f.web).byKey(t, "disk")); !equalStrings(got, []string{"/"}) {
		t.Fatalf("items after the edit = %v, want only the mount still asked for", got)
	}

	// History is not a current value, but it is still history: a graph of
	// a mount dropped last week is a real graph.
	series := requestJSON(t, f.viewerC, http.MethodGet,
		f.hostURL(f.web)+"/metrics?check=disk&item=/var&metric=used_percent&range=1h", nil)
	if series.StatusCode != http.StatusOK {
		t.Fatalf("series for the dropped mount = %d %s, want 200", series.StatusCode, mustReadBody(t, series))
	}
	var out struct {
		Points []struct {
			Value float64 `json:"value"`
		} `json:"points"`
	}
	decodeInto(t, series, &out)
	if len(out.Points) == 0 {
		t.Fatal("the dropped mount's samples are gone; only the current value was supposed to go")
	}
}

// TestHostChecksKeepsEveryItemWhenTheDefinitionBoundsNone: the other half
// of the rule. A disk check with no mounts list asks for every filesystem
// the host finds, so it bounds nothing and a mount that vanished from the
// host is the agent's silence — which the stale badge already says.
func TestHostChecksKeepsEveryItemWhenTheDefinitionBoundsNone(t *testing.T) {
	f := newChecksFixture(t, "host-checks-unbounded-items")
	now := time.Now()

	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: now, Value: floatPtr(41)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/srv", Metric: "used_percent", At: now, Value: floatPtr(3)},
		// cpu has no item at all and takes no parameters: nothing to bound.
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now, Value: floatPtr(12)},
	)

	body := f.hostChecks(t, f.viewerC, f.web)
	if got := itemsOf(body.byKey(t, "disk")); !equalStrings(got, []string{"/", "/srv"}) {
		t.Fatalf("disk items = %v, want every mount reported", got)
	}
	if got := itemsOf(body.byKey(t, "cpu")); !equalStrings(got, []string{""}) {
		t.Fatalf("cpu items = %v, want its one whole-host value", got)
	}
}

func itemsOf(c hostCheck) []string {
	out := make([]string, 0, len(c.Values))
	for _, v := range c.Values {
		out = append(out, v.Item)
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// seedTupleState writes one check_states row directly through the
// generated query the evaluator itself upserts through (gen.UpsertCheckState),
// exactly like internal/rules' own due_sql_test.go seeds check_current and
// check_samples rows for the store tests below rather than running an
// agent or a ticker: this file's job is the READ side (does the route
// surface a stored row correctly, and hide one it should not), not
// re-proving the evaluator wrote it correctly, which Task 8's and Task 9's
// own tests already do.
func seedTupleState(t *testing.T, s *apitest.Server, orgID, hostID uuid.UUID, checkKey, item, metric, state string, since, evaluatedAt time.Time, flapping bool) {
	t.Helper()
	if err := s.Q.UpsertCheckState(context.Background(), gen.UpsertCheckStateParams{
		OrgID: orgID, HostID: hostID, CheckKey: checkKey, Item: item, Metric: metric,
		State: state, Since: since, EvaluatedAt: evaluatedAt, Flapping: flapping,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestHostChecksReportsTupleState is Task 11's own addition to the
// v0.3b-1 route: state, since and flapping per tuple, additive only. Three
// things are pinned here that the design's controller ruled on explicitly
// (task-11-brief.md's pre-flight scan, against the plan's own text):
//
//   - a tuple check_states never evaluated reports state: null, not a
//     fabricated "ok" — a state is a statement about something observed;
//   - a stored row survives exactly as Task 8's evaluator left it once its
//     check stops resolving an interval (disabled, or deleted), but the
//     READ side must not show it as current — a stale crit from a check
//     that no longer runs must not read as a live one;
//   - and every v0.3b-1 field keeps reporting exactly what it always did.
func TestHostChecksReportsTupleState(t *testing.T) {
	f := newChecksFixture(t, "host-checks-state")
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-10 * time.Minute)

	// cpu, memory and service are all part of the organization's "Linux
	// base" seed set (checksFixture's own doc comment) — no check needs to
	// be created for this test.
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: now, Value: floatPtr(91)},
		checks.Observation{HostID: f.web, CheckKey: "memory", Metric: "used_percent", At: now, Value: floatPtr(40)},
		checks.Observation{HostID: f.web, CheckKey: "service", Item: "sshd", Metric: "state", At: now, Label: strPtr("active")},
	)
	seedTupleState(t, f.s, f.orgID, f.web, "cpu", "", "usage_percent", "crit", since, now, true)
	seedTupleState(t, f.s, f.orgID, f.web, "service", "sshd", "state", "warn", since, now, false)

	t.Run("a stored state reports state, since and flapping, and the v0.3b-1 fields did not move", func(t *testing.T) {
		cpu := f.hostChecks(t, f.viewerC, f.web).byKey(t, "cpu")
		if len(cpu.Values) != 1 {
			t.Fatalf("cpu values = %+v, want exactly one", cpu.Values)
		}
		v := cpu.Values[0]
		if v.Value == nil || *v.Value != 91 || v.Item != "" || v.Metric != "usage_percent" || v.Error != "" || v.Label != "" {
			t.Fatalf("cpu value = %+v, the v0.3b-1 fields must report exactly what they always did", v)
		}
		if v.State == nil || *v.State != "crit" {
			t.Fatalf("cpu state = %v, want crit", v.State)
		}
		if !v.Flapping {
			t.Fatal("cpu flapping = false, want true")
		}
		if v.Since == nil || !v.Since.Equal(since) {
			t.Fatalf("cpu since = %v, want %v", v.Since, since)
		}
	})

	t.Run("a tuple with no state row reports null, never a fabricated ok", func(t *testing.T) {
		memory := f.hostChecks(t, f.viewerC, f.web).byKey(t, "memory")
		if len(memory.Values) != 1 {
			t.Fatalf("memory values = %+v, want exactly one", memory.Values)
		}
		if memory.Values[0].State != nil {
			t.Fatalf("memory state = %v, want null: this tuple has never been evaluated", *memory.Values[0].State)
		}
		if memory.Values[0].Since != nil {
			t.Fatalf("memory since = %v, want none", *memory.Values[0].Since)
		}
		if memory.Values[0].Flapping {
			t.Fatal("memory flapping = true, want false: never evaluated")
		}
	})

	t.Run("a state row survives its check being disabled, but the read side hides it", func(t *testing.T) {
		var serviceDef checkDTO
		for _, d := range f.listChecks(t, f.ownerC) {
			if d.Key == "service" {
				serviceDef = d
			}
		}
		if serviceDef.ID == "" {
			t.Fatal("no seeded \"service\" check found")
		}
		// Disabling it at the host narrows nothing about check_states — the
		// row seeded above (severity warn) is left exactly as it was, which
		// is the whole point: Task 8's evaluator contract is a write-side
		// concern this test does not re-prove; only the read side is new
		// here.
		resp := requestJSON(t, f.operatorC, http.MethodPut, f.checksURL()+"/"+serviceDef.ID, map[string]any{
			"plugin": "service", "key": "service", "interval_seconds": 60,
			"binding_kind": "host", "host_id": f.web.String(),
			"params": map[string]any{"units": []string{"sshd"}}, "enabled": false,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("disable service at the host = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}

		service := f.hostChecks(t, f.viewerC, f.web).byKey(t, "service")
		if service.Enabled {
			t.Fatal("service = enabled, want the host's override to have turned it off")
		}
		if len(service.Values) != 1 {
			t.Fatalf("service values = %+v, want the current reading still listed", service.Values)
		}
		if service.Values[0].State != nil {
			t.Fatalf("service state = %v, want null: its check no longer resolves an interval", *service.Values[0].State)
		}
	})
}
