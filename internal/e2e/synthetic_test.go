// Backlog 3.4b, first half: a synthetic (server-run) check end to end. The
// manager's own probe runner — the one internal/app wires, not a runner
// this test builds — probes a target the test starts itself, records the
// result like an agent's, and the rule engine judges it:
//
//  1. An http check defined through the API on a monitored host at
//     127.0.0.1 reaches a recorded state: http.up = 1, status 200, ok.
//  2. Flipping the target to failing (it drops every connection without
//     answering) makes the state follow: http.up = 0 and crit, from the
//     seeded "HTTP check down" rule (http.up < 1), on a reading taken
//     after the flip.
//  3. Flipping it back makes the state follow again: http.up = 1, ok.
//
// The production probe guard refuses loopback (an SSRF control), so the
// manager is built with app.WithProbeGuard(probe.NewGuardForTest(true,
// nil)): loopback allowed, every other refused range still refused. The
// runner's tick and refresh come down from 5 s and 30 s through
// app.WithProbeTick and app.WithProbeRefresh, so a new definition and
// every due probe are picked up within about a second; the check's own
// interval stays at the 10 s minimum. No agent is involved: the host has
// none, which is exactly a synthetic check's case.
package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/probe"
)

// syntheticCheckKey is the http check's key on the host.
const syntheticCheckKey = "site"

// flippableTarget is the probed server: it answers 200 until failing is
// set, then drops every connection without a response, which the http
// probe reports as http.up = 0. hits counts every request it saw, so the
// test can tell the manager's probe really reached it.
type flippableTarget struct {
	failing atomic.Bool
	hits    atomic.Int64
}

func (f *flippableTarget) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.hits.Add(1)
	if !f.failing.Load() {
		_, _ = fmt.Fprint(w, "ok")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

// syntheticTuple reads one metric of the host's synthetic check off the
// host page, the same route cpuTupleState (rules_test.go) reads; ok is
// false while the page carries no such tuple yet.
func syntheticTuple(t *testing.T, w agentWorld, hostID, metric string) (ruleTupleOut, bool) {
	t.Helper()
	var out struct {
		Checks []ruleHostCheckOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/checks", nil, &out)
	for _, c := range out.Checks {
		if c.Key != syntheticCheckKey {
			continue
		}
		for _, v := range c.Values {
			if v.Metric == metric {
				return v, true
			}
		}
	}
	return ruleTupleOut{}, false
}

// waitForSynthetic polls the host page until http.up reads wantUp with
// state wantState, on a reading taken at or after notBefore, and returns
// that reading.
func waitForSynthetic(t *testing.T, w agentWorld, hostID string, wantUp float64, wantState string, notBefore time.Time, within time.Duration) ruleTupleOut {
	t.Helper()
	deadline := time.Now().Add(within)
	var last ruleTupleOut
	var seen bool
	for {
		last, seen = syntheticTuple(t, w, hostID, "http.up")
		if seen && last.Value != nil && *last.Value == wantUp && last.State != nil && *last.State == wantState &&
			!last.At.Before(notBefore) {
			return last
		}
		if time.Now().After(deadline) {
			got := "no http.up tuple"
			if seen {
				got = fmt.Sprintf("value %s, state %s, at %s", ptrText(last.Value), ptrText(last.State), last.At.Format(time.RFC3339Nano))
			}
			t.Fatalf("timed out after %s waiting for http.up = %v at %s (reading at or after %s); last: %s",
				within, wantUp, wantState, notBefore.Format(time.RFC3339Nano), got)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// ptrText renders an optional value for a failure message.
func ptrText[T any](p *T) string {
	if p == nil {
		return "<none>"
	}
	return fmt.Sprint(*p)
}

func TestSyntheticCheckEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := &flippableTarget{}
	srv := httptest.NewServer(target)
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	w := wireAgentManagerWith(t, ctx, []app.Option{
		app.WithProbeGuard(probe.NewGuardForTest(true, nil)),
		app.WithProbeTick(200 * time.Millisecond),
		app.WithProbeRefresh(time.Second),
	})
	prefix := "/api/v1/orgs/" + w.org

	// --- 1. Definition to a recorded state ----------------------------------
	var host hostOut
	w.api.do(http.MethodPost, prefix+"/hosts", map[string]any{
		"name": "e2e-synthetic", "zone_id": w.zoneID, "address": "127.0.0.1", "monitored": true,
	}, &host)
	hostID := host.Host.ID
	defined := time.Now().UTC().Truncate(time.Microsecond)
	w.api.do(http.MethodPost, prefix+"/checks", map[string]any{
		"key": syntheticCheckKey, "plugin": "http", "params": map[string]any{"url": "http://{address}:" + port + "/"},
		"interval_seconds": 10, "enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)
	// One interval for the first probe at the latest, plus the refresh,
	// the evaluation tick and generous slack.
	waitForSynthetic(t, w, hostID, 1, "ok", defined, 60*time.Second)
	if target.hits.Load() == 0 {
		t.Fatal("http.up = 1 was recorded but the test's target saw no request")
	}
	if status, ok := syntheticTuple(t, w, hostID, "http.status_code"); !ok || status.Value == nil || *status.Value != http.StatusOK {
		t.Fatalf("http.status_code tuple = %+v (present %v), want 200", status, ok)
	}

	// --- 2. The target fails, the state follows ----------------------------
	hitsBefore := target.hits.Load()
	target.failing.Store(true)
	flipped := time.Now().UTC().Truncate(time.Microsecond)
	waitForSynthetic(t, w, hostID, 0, "crit", flipped, 60*time.Second)
	if target.hits.Load() == hitsBefore {
		t.Fatal("http.up = 0 was recorded but the failing target saw no request after the flip")
	}

	// --- 3. The target recovers, the state follows -------------------------
	target.failing.Store(false)
	recovered := time.Now().UTC().Truncate(time.Microsecond)
	waitForSynthetic(t, w, hostID, 1, "ok", recovered, 60*time.Second)
}
