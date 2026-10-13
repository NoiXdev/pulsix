// The v0.4b milestone gate: an alert route's own promise, end to end — a
// route that covers a host at crit/opened reaches the channel it names,
// and two alerts that open in the same tick arrive as one message, not
// two (spec decision 5's own coalescing). Built on the same real-agent
// harness checks_test.go and rules_test.go already wire
// (wireAgentManager): the real agent reports genuine cpu and memory
// readings, and two always-crossed, no-ripening crit rules are what put
// each tuple's alert into the state this gate drives — the exact lever
// rules_test.go's own header comment explains, generalized from one
// metric to two so there are two alerts to batch.
//
// The follower itself is not the one the running manager already starts:
// internal/app wires alertroutes.Follower.Run on a hardcoded one-minute
// ticker with no injectable clock (see internal/app's own comment on
// stopAlertNotify) and no app.Option reaches it, so a test cannot speed
// it up. This scenario therefore opens its own *db.DB against the same
// shared test database (testdb.Open — the exact call
// internal/alertroutes's own follow_test.go already makes, for the same
// reason) and builds its own Follower, ticked once, directly, on demand.
// This does not race the real one in any way that matters: both would
// reach the same answer if they ever overlapped — the ledger's anti-join
// and the per-organization advisory lock make that safe by construction
// (follow.go's own doc comment) — calling Tick directly only keeps this
// gate's timing under the test's control rather than at the mercy of a
// one-minute wall clock.
//
// What this does NOT assert: that the real manager's own background
// follower is wired correctly end to end — internal/app's wiring is not
// under test here, only internal/alertroutes' own Follower and the API
// surface an operator actually drives (channels, routes, alerts).
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
)

// noGroupsReader is the GroupReader this gate's own Follower needs: both
// routes it drives target the host directly (scope "host"), and
// scopeCovers (alertroutes package) never consults group membership for a
// host-scoped route — the same "no groups to resolve" fixture
// internal/alertroutes' own follow_test.go calls noGroups.
type noGroupsReader struct{}

func (noGroupsReader) GroupsForHost(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// alertRouteMemMetric is mem.used_pct's own catalogue key
// (internal/rules/catalog.go) — the second metric this gate drives into
// crit, alongside checks_test.go's own cpuMetric, so there are two
// distinct alerts for the follower to batch.
const alertRouteMemMetric = "mem.used_pct"

// createCritRule creates one host-scoped, form-mode crit rule over metric
// with no ripening (for_seconds 0) and a threshold no real percentage can
// fail (belowAnyPercentage, rules_test.go's own always-crossed end of the
// lever its header comment explains) — generalized from createFormRule
// over any metric, since this gate needs two. With no warn rule at all
// over this metric and no ripening delay, the tuple's very first
// evaluation already reads crit, so alerts.Reconcile opens the alert
// directly at crit (ActionOpen carries the tuple's own current State)
// rather than opening at warn and raising afterward.
func createCritRule(t *testing.T, w agentWorld, hostID, name, metric string) {
	t.Helper()
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/rules", map[string]any{
		"name": name, "enabled": true, "scope": "host", "host_id": hostID,
		"metric": metric, "severity": "crit", "mode": "form",
		"form":        map[string]any{"metric": metric, "op": ">", "value": belowAnyPercentage},
		"for_seconds": 0,
	}, nil)
}

// alertRouteTupleState reads one (check key, metric) tuple's current rule
// verdict off the host page, the same route cpuTupleState (rules_test.go)
// reads, generalized over any check key and metric since this gate
// watches two tuples rather than one.
func alertRouteTupleState(t *testing.T, w agentWorld, hostID, checkKey, metric string) *string {
	t.Helper()
	var out struct {
		Checks []ruleHostCheckOut `json:"checks"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID+"/checks", nil, &out)
	for _, c := range out.Checks {
		if c.Key != checkKey {
			continue
		}
		for _, v := range c.Values {
			if v.Metric == metric {
				return v.State
			}
		}
	}
	return nil
}

// waitForBothCrit polls the host page, the way an operator watching it
// would, until both the cpu and memory tuples read crit.
func waitForBothCrit(t *testing.T, w agentWorld, hostID string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	tuples := [2][2]string{{"cpu", cpuMetric}, {"memory", alertRouteMemMetric}}
	for {
		allCrit := true
		for _, tp := range tuples {
			if state := alertRouteTupleState(t, w, hostID, tp[0], tp[1]); state == nil || *state != "crit" {
				allCrit = false
				break
			}
		}
		if allCrit {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for both cpu and memory to reach crit", within)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// alertListOut is GET .../alerts' own list shape, the fields this gate
// needs to confirm both alerts actually opened before blaming a missing
// delivery on the follower rather than on the alerts pipeline underneath
// it.
type alertListOut struct {
	Alerts []struct {
		CheckKey string `json:"check_key"`
		Severity string `json:"severity"`
	} `json:"alerts"`
}

func TestAlertRoutesEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx, netip.MustParsePrefix("127.0.0.0/8"))
	prefix := "/api/v1/orgs/" + w.org

	// --- A real agent, two host-bound checks, two tuples driven to crit ---
	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-alert-routes"), "e2e-alert-routes")
	defer stop()
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	for _, key := range []string{"cpu", "memory"} {
		w.api.do(http.MethodPost, prefix+"/checks", map[string]any{
			"key": key, "plugin": key, "interval_seconds": 10,
			"enabled": true, "binding_kind": "host", "host_id": hostID,
		}, nil)
	}
	createCritRule(t, w, hostID, "e2e-route-cpu-crit", cpuMetric)
	createCritRule(t, w, hostID, "e2e-route-mem-crit", alertRouteMemMetric)
	waitForBothCrit(t, w, hostID, 60*time.Second)

	var opened alertListOut
	w.api.do(http.MethodGet, prefix+"/alerts?host="+hostID+"&state=open", nil, &opened)
	var sawCPU, sawMem bool
	for _, a := range opened.Alerts {
		if a.CheckKey == "cpu" && a.Severity == "crit" {
			sawCPU = true
		}
		if a.CheckKey == "memory" && a.Severity == "crit" {
			sawMem = true
		}
	}
	if !sawCPU || !sawMem {
		t.Fatalf("open alerts on the host = %+v, want both cpu and memory open at crit", opened.Alerts)
	}

	// --- A channel, and a route covering this host at crit/opened --------
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()
	var ch channelCreatedOut
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e alert receiver",
		"webhook": map[string]any{"url": hook.URL + "/pulsix", "signed": false},
	}, &ch)
	w.api.do(http.MethodPost, prefix+"/alert-routes", map[string]any{
		"name": "e2e crit route", "enabled": true, "scope": "host", "scope_id": hostID,
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{ch.Channel.ID},
	}, nil)

	// --- Run the follower's own Tick directly, against the same database -
	var orgOut struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	w.api.do(http.MethodGet, prefix, nil, &orgOut)
	orgID := uuid.MustParse(orgOut.Org.ID)
	channelID := uuid.MustParse(ch.Channel.ID)

	d := testdb.Open(t)
	store := alertroutes.NewSQLStore(d)
	outbox := notify.NewOutbox(d, "http://e2e-alert-routes.test", time.Now, nil, nil)
	follower := alertroutes.NewFollower(d, store, noGroupsReader{}, oncall.NewSQLStore(d), outbox,
		"http://e2e-alert-routes.test", time.Now, nil)
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick: %v", err)
	}

	// --- Exactly one notification_deliveries row, naming both alerts -----
	// Read via the pool directly: toNotifyDeliveryDTO (the GET
	// .../deliveries HTTP route) does not expose payload at all, and the
	// claim under test — one row, carrying both alerts' own data — lives
	// entirely in that column.
	var count int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM notification_deliveries WHERE org_id = $1 AND channel_id = $2`,
		orgID, channelID).Scan(&count); err != nil {
		t.Fatalf("count notification_deliveries: %v", err)
	}
	if count != 1 {
		t.Fatalf("notification_deliveries rows for the channel = %d, want exactly 1 (decision 5: one message per channel per tick, whatever the batch inside it)", count)
	}
	var payload []byte
	if err := d.Pool().QueryRow(ctx,
		`SELECT payload FROM notification_deliveries WHERE org_id = $1 AND channel_id = $2`,
		orgID, channelID).Scan(&payload); err != nil {
		t.Fatalf("read notification_deliveries payload: %v", err)
	}
	var ev notify.Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		t.Fatalf("decode delivery payload: %v", err)
	}
	if ev.Kind != notify.EventAlertBatch || ev.Alerts == nil || ev.Alerts.Count != len(ev.Alerts.Items) {
		t.Fatalf("delivery payload = %+v, want one alert.batch message whose own Count matches its own Items", ev)
	}
	var gotCPU, gotMem bool
	for _, item := range ev.Alerts.Items {
		if item.Severity != "crit" || item.Event != "opened" {
			t.Errorf("batch item = %+v, want severity crit, event opened — the route's own selections", item)
		}
		switch item.CheckKey {
		case "cpu":
			gotCPU = true
		case "memory":
			gotMem = true
		}
	}
	if !gotCPU || !gotMem {
		t.Fatalf("the one delivery's items = %+v, want both the cpu and the memory alert named in it", ev.Alerts.Items)
	}

	// --- The real app's own dispatcher, already running, actually sends
	//     the row the follower wrote: a follower-written row is not merely
	//     well-formed, it is deliverable through the same path a
	//     schedule-notification row already travels ------------------------
	deadline := time.Now().Add(30 * time.Second)
	for len(rec.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the webhook channel received nothing in 30s")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
