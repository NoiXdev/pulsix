// The v0.4c milestone gate: an alert route that draws its channel from an
// on-call rotation and limits delivery to a window, end to end — on the
// same real-agent harness alertroutes_test.go already wires. A route whose
// window is shut holds the alert; advancing the follower's own injected
// clock past both the handover and the window's opening delivers it to
// whoever is on call AT THAT INSTANT, not whoever was on call when the
// alert opened (decision 8). internal/alertroutes' own
// TestRotationIsResolvedAtDeliveryTimeNotEventTime
// (follow_window_test.go) pins the same claim at the unit level, against a
// fixture alert; this gate is its end-to-end twin, driven through the HTTP
// API an operator actually uses — a channel, a rotation, a route — and a
// real crit alert a real agent's own reading opens.
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

// oncallRotationCreatedOut is POST .../oncall-rotations' own response
// shape — only the id this gate needs, to point a route's rotation_id at
// it.
type oncallRotationCreatedOut struct {
	Rotation struct {
		ID string `json:"id"`
	} `json:"rotation"`
}

func TestOncallEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx, netip.MustParsePrefix("127.0.0.0/8"))
	prefix := "/api/v1/orgs/" + w.org

	// --- A real agent, host-bound, driven to crit --------------------------
	stop := startAgent(t, w, t.TempDir(), w.createToken(t, "", "e2e-oncall"), "e2e-oncall")
	defer stop()
	list := waitForAgent(t, w, "the monitored agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online"
	})
	hostID := list[0].HostID

	w.api.do(http.MethodPost, prefix+"/checks", map[string]any{
		"key": "cpu", "plugin": "cpu", "interval_seconds": 10,
		"enabled": true, "binding_kind": "host", "host_id": hostID,
	}, nil)
	createFormRule(t, w, hostID, "e2e-oncall-crit", "crit", belowAnyPercentage, 0)
	waitForTupleState(t, w, hostID, "crit", "the always-crossed crit threshold", 60*time.Second)

	// --- Two channels and a one-day rotation cycling through both ----------
	// Day 0 (anchor) is the first channel's shift, day 1 the second's — the
	// handover this gate's second tick must cross.
	var chFirst, chSecond channelCreatedOut
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e oncall first",
		"webhook": map[string]any{"url": "http://127.0.0.1:1/first", "signed": false},
	}, &chFirst)
	w.api.do(http.MethodPost, prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e oncall second",
		"webhook": map[string]any{"url": "http://127.0.0.1:1/second", "signed": false},
	}, &chSecond)

	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rot oncallRotationCreatedOut
	w.api.do(http.MethodPost, prefix+"/oncall-rotations", map[string]any{
		"name": "e2e oncall rotation", "period_days": 1,
		"anchor": anchor.Format(time.RFC3339), "timezone": "UTC",
		"channels": []string{chFirst.Channel.ID, chSecond.Channel.ID},
	}, &rot)

	// --- A route drawing its channel from the rotation, with a daily
	//     09:00-10:00 UTC delivery window ------------------------------------
	w.api.do(http.MethodPost, prefix+"/alert-routes", map[string]any{
		"name": "e2e oncall route", "enabled": true, "scope": "host", "scope_id": hostID,
		"severities": []string{"crit"}, "events": []string{"opened"},
		"rotation_id": rot.Rotation.ID,
		"window":      map[string]any{"cron": "0 9 * * *", "duration_seconds": 3600, "timezone": "UTC"},
	}, nil)

	// --- Run the follower's own Tick directly, against the same database,
	//     on a clock this test controls entirely — the follower needs no
	//     wall-clock wait to cross a handover or a window's own open/shut
	//     boundary -------------------------------------------------------
	var orgOut struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	w.api.do(http.MethodGet, prefix, nil, &orgOut)
	orgID := uuid.MustParse(orgOut.Org.ID)
	channelFirstID := uuid.MustParse(chFirst.Channel.ID)
	channelSecondID := uuid.MustParse(chSecond.Channel.ID)

	d := testdb.Open(t)
	store := alertroutes.NewSQLStore(d)
	outbox := notify.NewOutbox(d, "http://e2e-oncall.test", time.Now, nil, nil)
	var now time.Time
	clock := func() time.Time { return now }
	follower := alertroutes.NewFollower(d, store, noGroupsReader{}, oncall.NewSQLStore(d), outbox,
		"http://e2e-oncall.test", clock, nil)

	countDeliveries := func() int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx,
			`SELECT count(*) FROM notification_deliveries WHERE org_id = $1`, orgID).Scan(&n); err != nil {
			t.Fatalf("count notification_deliveries: %v", err)
		}
		return n
	}

	// --- Day 0, 08:00 UTC: the first channel's own shift, the window shut
	//     an hour away — the alert is held, not dropped -----------------
	now = anchor.Add(8 * time.Hour)
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick while the window was shut: %v", err)
	}
	if n := countDeliveries(); n != 0 {
		t.Fatalf("notification_deliveries rows while the window was shut = %d, want 0", n)
	}

	// --- Day 1, 09:30 UTC: past the handover to the second channel AND
	//     inside the window — the held alert is caught up and reaches
	//     whoever is on call NOW, not whoever was on call when it opened --
	now = anchor.AddDate(0, 0, 1).Add(9*time.Hour + 30*time.Minute)
	if err := follower.Tick(ctx, orgID); err != nil {
		t.Fatalf("follower tick once the window opened: %v", err)
	}
	if n := countDeliveries(); n != 1 {
		t.Fatalf("notification_deliveries rows once the window opened = %d, want exactly 1", n)
	}
	var gotChannel uuid.UUID
	if err := d.Pool().QueryRow(ctx,
		`SELECT channel_id FROM notification_deliveries WHERE org_id = $1`, orgID).Scan(&gotChannel); err != nil {
		t.Fatalf("read notification_deliveries channel: %v", err)
	}
	if gotChannel != channelSecondID {
		t.Fatalf("delivered to channel %s, want the second channel %s — whoever is on call at delivery time, "+
			"not the first channel %s that was on call when the alert opened",
			gotChannel, channelSecondID, channelFirstID)
	}
}
