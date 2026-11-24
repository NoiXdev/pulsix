// Backlog 3.4b, second half: the notification sent when an alert
// resolves, end to end. TestAlertRoutesEndToEnd (alertroutes_test.go)
// proves an opened alert reaches a channel; nothing proved that the same
// channel is told when the condition clears. This gate drives one alert
// through its whole life on the real-agent harness (wireAgentManager):
//
//  1. A route covering the host at crit for both "opened" and "closed"
//     sends a firing message the moment the follower ticks after a crit
//     rule opens the alert.
//  2. When the condition clears (the rule's threshold moves out of reach,
//     the same lever rules_test.go's header comment explains), the alert
//     closes, and the next follower tick sends a second, separate message
//     whose item for that alert reads event "closed": the resolve
//     notification, received by the webhook the test started itself.
//
// The follower is ticked directly, exactly as TestAlertRoutesEndToEnd and
// TestEscalationEndToEnd do and for the same reason (internal/app runs its
// own on a hardcoded one-minute ticker with no seam). The running app's
// follower may still tick in between; the ledger decides every event once
// (follow.go), so whichever follower decides an event, the channel hears
// about it exactly once — which is what the item counts below assert. The
// route and channel exist before the rule does, so no follower can decide
// the opening event before there is a route to send it to.
//
// Only the items naming this test's own alert are judged: the seed rules
// (internal/rules/seed.go) judge this machine's genuine readings and could
// open an alert of their own on the host, which the route would forward
// too.
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

// alertMessage is one webhook request decoded as the alert batch it
// carries.
type alertMessage struct {
	received
	ev notify.Event
}

// alertItemsFor decodes every request rec received so far and returns,
// in arrival order, the messages carrying at least one item about
// alertID, together with how many items name it per event ("opened",
// "closed", …) across all of them.
func alertItemsFor(t *testing.T, rec *hookReceiver, alertID uuid.UUID) ([]alertMessage, map[string]int) {
	t.Helper()
	var msgs []alertMessage
	counts := map[string]int{}
	for _, r := range rec.snapshot() {
		var ev notify.Event
		if err := json.Unmarshal(r.body, &ev); err != nil {
			t.Fatalf("webhook body is not an event: %v: %s", err, r.body)
		}
		if ev.Alerts == nil {
			continue
		}
		mine := false
		for _, item := range ev.Alerts.Items {
			if item.AlertID == alertID {
				counts[item.Event]++
				mine = true
			}
		}
		if mine {
			msgs = append(msgs, alertMessage{received: r, ev: ev})
		}
	}
	return msgs, counts
}

// awaitAlertEvent polls the receiver until an item about alertID with the
// given event has arrived, and returns the message carrying it.
func awaitAlertEvent(t *testing.T, rec *hookReceiver, alertID uuid.UUID, event string, within time.Duration) alertMessage {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		msgs, _ := alertItemsFor(t, rec, alertID)
		for _, m := range msgs {
			for _, item := range m.ev.Alerts.Items {
				if item.AlertID == alertID && item.Event == event {
					return m
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the webhook received no %q item for alert %s in %s; %d request(s) in all",
				event, alertID, within, len(rec.snapshot()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// awaitAlertClosed polls the host's closed alerts until alertID is among
// them — the alert pipeline's own answer, so a missing resolve message
// below is blamed on the follower or the dispatcher, not on an alert that
// never closed.
func awaitAlertClosed(t *testing.T, w agentWorld, hostID, alertID string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var closed escalationAlertListOut
		w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/alerts?host="+hostID+"&state=closed&limit=100", nil, &closed)
		for _, a := range closed.Alerts {
			if a.ID == alertID {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("alert %s did not close within %s; closed alerts on the host = %+v", alertID, within, closed.Alerts)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestAlertResolveNotificationEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx, netip.MustParsePrefix("127.0.0.0/8"))
	prefix := "/api/v1/orgs/" + w.org

	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-resolve"), "e2e-resolve")
	defer stop()
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	// --- The channel and the route, before anything can open -------------
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()
	var ch channelCreatedOut
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e resolve receiver",
		"webhook": map[string]any{"url": hook.URL + "/pulsix", "signed": false},
	}, &ch)
	w.api.do(http.MethodPost, prefix+"/alert-routes", map[string]any{
		"name": "e2e resolve route", "enabled": true, "scope": "host", "scope_id": hostID,
		"severities": []string{"crit"}, "events": []string{"opened", "closed"},
		"channels": []string{ch.Channel.ID},
	}, nil)

	// --- A host-bound cpu check and an always-crossed crit rule ----------
	w.api.do(http.MethodPost, prefix+"/checks", map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": int(ruleCheckInterval / time.Second),
		"enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)
	critID := createFormRule(t, w, hostID, "e2e-resolve-crit", "crit", belowAnyPercentage, 0)
	waitForTupleState(t, w, hostID, "crit", "the always-crossed crit threshold", 60*time.Second)

	var opened escalationAlertListOut
	w.api.do(http.MethodGet, prefix+"/alerts?host="+hostID+"&state=open", nil, &opened)
	var alertIDStr string
	for _, a := range opened.Alerts {
		if a.CheckKey == "cpu" && a.Severity == "crit" {
			alertIDStr = a.ID
		}
	}
	if alertIDStr == "" {
		t.Fatalf("open alerts on the host = %+v, want the cpu alert at crit", opened.Alerts)
	}
	alertID := uuid.MustParse(alertIDStr)

	// --- The follower, ticked on demand against the same database --------
	var orgOut struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	w.api.do(http.MethodGet, prefix, nil, &orgOut)
	orgID := uuid.MustParse(orgOut.Org.ID)
	d := testdb.Open(t)
	outbox := notify.NewOutbox(d, "http://e2e-resolve.test", time.Now, nil, nil)
	follower := alertroutes.NewFollower(d, alertroutes.NewSQLStore(d), noGroupsReader{}, oncall.NewSQLStore(d), outbox,
		"http://e2e-resolve.test", time.Now, nil)

	// --- 1. The firing message ---------------------------------------------
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick after the alert opened: %v", err)
	}
	firing := awaitAlertEvent(t, rec, alertID, "opened", 30*time.Second)
	if firing.event != string(notify.EventAlertBatch) || firing.ev.Kind != notify.EventAlertBatch {
		t.Fatalf("firing message: header event %q, kind %q, want both %q", firing.event, firing.ev.Kind, notify.EventAlertBatch)
	}
	if _, counts := alertItemsFor(t, rec, alertID); counts["opened"] != 1 || counts["closed"] != 0 {
		t.Fatalf("items about the alert after it opened = %v, want exactly one opened and no closed", counts)
	}

	// --- 2. The condition clears, the alert closes -------------------------
	putFormThreshold(t, w, critID, hostID, "e2e-resolve-crit", "crit", aboveAnyPercentage, 0)
	waitForTupleState(t, w, hostID, "ok", "the crit threshold moved out of reach", 60*time.Second)
	awaitAlertClosed(t, w, hostID, alertIDStr, 30*time.Second)

	// --- 3. The resolve message ----------------------------------------------
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick after the alert closed: %v", err)
	}
	resolved := awaitAlertEvent(t, rec, alertID, "closed", 30*time.Second)
	if resolved.event != string(notify.EventAlertBatch) || resolved.ev.Kind != notify.EventAlertBatch {
		t.Fatalf("resolve message: header event %q, kind %q, want both %q", resolved.event, resolved.ev.Kind, notify.EventAlertBatch)
	}
	if resolved.delivery == "" || resolved.delivery == firing.delivery {
		t.Fatalf("resolve message delivery %q, firing %q: want a delivery of its own", resolved.delivery, firing.delivery)
	}
	var item *notify.AlertItem
	for i := range resolved.ev.Alerts.Items {
		if it := &resolved.ev.Alerts.Items[i]; it.AlertID == alertID && it.Event == "closed" {
			item = it
		}
	}
	if item == nil {
		t.Fatalf("resolve message items = %+v, want a closed item for alert %s", resolved.ev.Alerts.Items, alertID)
	}
	if item.HostID.String() != hostID || item.CheckKey != "cpu" || item.Severity != "crit" ||
		resolved.ev.Alerts.Count != len(resolved.ev.Alerts.Items) {
		t.Fatalf("resolve item = %+v (batch count %d of %d items), want host %s, check cpu, severity crit",
			*item, resolved.ev.Alerts.Count, len(resolved.ev.Alerts.Items), hostID)
	}
	if _, counts := alertItemsFor(t, rec, alertID); counts["opened"] != 1 || counts["closed"] != 1 {
		t.Fatalf("items about the alert in the end = %v, want exactly one opened and one closed", counts)
	}
}
