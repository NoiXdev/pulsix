// The v0.4d milestone gate: an escalation chain climbs its ladder, one
// rung at a time, and stops the instant a human acknowledges — end to
// end, through a real agent's own crit alert, driven on the same
// real-agent harness alertroutes_test.go and oncall_test.go already wire
// (wireAgentManager). It follows TestOncallEndToEnd's shape exactly: the
// follower's own Tick is called directly, against its own *db.DB, on a
// clock this test controls entirely, so the design's minute-scale delays
// cost no wall-clock time.
//
// Four claims, in the order the design's acceptance list makes them
// (2026-10-16-v0.4d-escalation-chains-design.md):
//
//  1. The route's base delivery (step 0, decision 1) reaches the first
//     channel the moment the crit alert opens.
//  2. Advancing the clock past the first rung's delay with no
//     acknowledgement delivers to the second channel — and ONLY the
//     second: the first channel's own delivery count does not move, and
//     ticking again at the identical instant does not deliver a second
//     time (decision 6: a step is reached once).
//  3. Acknowledging the alert (decision 2) stops the chain.
//  4. Advancing the clock past the next rung's own delay, after the
//     acknowledgement, delivers nothing: the third channel — the one the
//     ladder's own second step names — never receives anything at all.
package e2e

import (
	"context"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
)

// escalationAlertListOut is GET .../alerts' own list shape, with the id
// this gate needs beyond what alertListOut (alertroutes_test.go) already
// reads, to acknowledge the one alert the crit rule opened.
type escalationAlertListOut struct {
	Alerts []struct {
		ID       string `json:"id"`
		CheckKey string `json:"check_key"`
		Severity string `json:"severity"`
	} `json:"alerts"`
}

func TestEscalationEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx, netip.MustParsePrefix("127.0.0.0/8"))
	prefix := "/api/v1/orgs/" + w.org

	// --- A real agent, host-bound, driven to crit --------------------------
	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-escalation"), "e2e-escalation")
	defer stop()
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	w.api.do(http.MethodPost, prefix+"/checks", map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": 10,
		"enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)
	createFormRule(t, w, hostID, "e2e-escalation-crit", "crit", belowAnyPercentage, 0)
	waitForTupleState(t, w, hostID, "crit", "the always-crossed crit threshold", 60*time.Second)

	var opened escalationAlertListOut
	w.api.do(http.MethodGet, prefix+"/alerts?host="+hostID+"&state=open", nil, &opened)
	var alertID string
	for _, a := range opened.Alerts {
		if a.CheckKey == "cpu" && a.Severity == "crit" {
			alertID = a.ID
		}
	}
	if alertID == "" {
		t.Fatalf("open alerts on the host = %+v, want the cpu alert at crit", opened.Alerts)
	}

	// --- Three channels: the route's own base delivery, and its two-rung
	//     escalation ladder ------------------------------------------------
	var chBase, chStep1, chStep2 channelCreatedOut
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e escalation base",
		"webhook": map[string]any{"url": "http://127.0.0.1:1/base", "signed": false},
	}, &chBase)
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e escalation step 1",
		"webhook": map[string]any{"url": "http://127.0.0.1:1/step1", "signed": false},
	}, &chStep1)
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e escalation step 2",
		"webhook": map[string]any{"url": "http://127.0.0.1:1/step2", "signed": false},
	}, &chStep2)

	// --- A route whose base is the first channel, and whose ladder climbs
	//     to the second and then the third, five minutes apart ------------
	const stepDelay = 5 * time.Minute
	w.api.do(http.MethodPost, prefix+"/alert-routes", map[string]any{
		"name": "e2e escalation route", "enabled": true, "scope": "host", "scope_id": hostID,
		"severities": []string{"crit"}, "events": []string{"opened"},
		"channels": []string{chBase.Channel.ID},
		"steps": []map[string]any{
			{"position": 1, "delay_seconds": int64(stepDelay / time.Second), "channel_ids": []string{chStep1.Channel.ID}},
			{"position": 2, "delay_seconds": int64(stepDelay / time.Second), "channel_ids": []string{chStep2.Channel.ID}},
		},
	}, nil)

	// --- Run the follower's own Tick directly, against the same database,
	//     on a clock this test controls entirely — exactly
	//     TestOncallEndToEnd's own shape (oncall_test.go) ------------------
	var orgOut struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	w.api.do(http.MethodGet, prefix, nil, &orgOut)
	orgID := uuid.MustParse(orgOut.Org.ID)
	channelBaseID := uuid.MustParse(chBase.Channel.ID)
	channelStep1ID := uuid.MustParse(chStep1.Channel.ID)
	channelStep2ID := uuid.MustParse(chStep2.Channel.ID)

	d := testdb.Open(t)
	store := alertroutes.NewSQLStore(d)
	outbox := notify.NewOutbox(d, "http://e2e-escalation.test", time.Now, nil, nil)
	var now time.Time
	clock := func() time.Time { return now }
	follower := alertroutes.NewFollower(d, store, noGroupsReader{}, oncall.NewSQLStore(d), outbox,
		"http://e2e-escalation.test", clock, nil)

	countDeliveries := func(channelID uuid.UUID) int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx,
			`SELECT count(*) FROM notification_deliveries WHERE org_id = $1 AND channel_id = $2`,
			orgID, channelID).Scan(&n); err != nil {
			t.Fatalf("count notification_deliveries for channel %s: %v", channelID, err)
		}
		return n
	}

	// --- Claim 1: the base delivery reaches the first channel the moment
	//     the crit alert is ticked, before any delay has had a chance to
	//     elapse ---------------------------------------------------------
	now = time.Now()
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick for the base delivery: %v", err)
	}
	if n := countDeliveries(channelBaseID); n != 1 {
		t.Fatalf("base channel deliveries after the opening tick = %d, want exactly 1", n)
	}
	if n := countDeliveries(channelStep1ID); n != 0 {
		t.Fatalf("step 1 channel deliveries after the opening tick = %d, want 0 (its delay has not elapsed)", n)
	}
	if n := countDeliveries(channelStep2ID); n != 0 {
		t.Fatalf("step 2 channel deliveries after the opening tick = %d, want 0 (its delay has not elapsed)", n)
	}

	// --- Claim 2: advancing past the first rung's own delay with no
	//     acknowledgement reaches the second channel, and ONLY the second
	//     — the base channel does not receive the alert again, and ticking
	//     again at the identical instant does not fire the rung twice
	//     (decision 6: a step is reached once) ----------------------------
	now = now.Add(stepDelay + time.Minute)
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick past step 1's delay: %v", err)
	}
	if n := countDeliveries(channelBaseID); n != 1 {
		t.Fatalf("base channel deliveries after step 1 fired = %d, want still exactly 1 (not re-delivered)", n)
	}
	if n := countDeliveries(channelStep1ID); n != 1 {
		t.Fatalf("step 1 channel deliveries once its delay elapsed = %d, want exactly 1", n)
	}
	if n := countDeliveries(channelStep2ID); n != 0 {
		t.Fatalf("step 2 channel deliveries while step 1 was only just reached = %d, want 0", n)
	}
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick again at the same instant: %v", err)
	}
	if n := countDeliveries(channelStep1ID); n != 1 {
		t.Fatalf("step 1 channel deliveries after a second tick at the same instant = %d, want still exactly 1 (a rung fires once)", n)
	}

	// --- Claim 3: acknowledging the alert stops the chain (decision 2 and
	//     decision 4's first row) ----------------------------------------
	w.api.do(http.MethodPost, prefix+"/alerts/"+alertID+"/acknowledge", map[string]any{}, nil)

	// --- Claim 4: advancing past the second rung's own delay, after the
	//     acknowledgement, delivers nothing — the third channel, which only
	//     the ladder's second step names, never receives anything at all --
	now = now.Add(stepDelay + time.Minute)
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick past step 2's delay, after acknowledgement: %v", err)
	}
	if n := countDeliveries(channelStep2ID); n != 0 {
		t.Fatalf("step 2 channel deliveries after acknowledgement = %d, want 0 — the acknowledgement stopped the chain", n)
	}
	if n := countDeliveries(channelBaseID); n != 1 {
		t.Fatalf("base channel deliveries at the end = %d, want still exactly 1", n)
	}
	if n := countDeliveries(channelStep1ID); n != 1 {
		t.Fatalf("step 1 channel deliveries at the end = %d, want still exactly 1", n)
	}
}
