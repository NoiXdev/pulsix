package agents_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
)

// TestOnlineSinceTracksTheOnlinePeriod walks one agent through the start
// of its current online period (host_agents.online_since, backlog 3.57),
// which a client host's staleness is measured from: NULL after
// enrollment, set by the first touch, kept by every touch or heartbeat
// that follows the previous one within agents.StaleAfter (the agents
// list's online window), set again after a longer gap — an overnight
// power-off starts a new period, unlike connected_since — set by the
// first touch after the upgrade, and NULL after a registration (renewal or
// re-enrollment) until the first touch under the new serial sets it.
func TestOnlineSinceTracksTheOnlinePeriod(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-online-since")
	h := createHost(t, d, org, zone, "nb-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9401")); err != nil {
		t.Fatal(err)
	}
	read := func() (online, seen, connected *time.Time) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx,
			`SELECT online_since, last_seen, connected_since FROM host_agents WHERE host_id = $1`, h.ID,
		).Scan(&online, &seen, &connected); err != nil {
			t.Fatal(err)
		}
		return online, seen, connected
	}
	// rewind moves last_seen back by seenAgo and sets online_since to
	// onlineAgo before now (NULL when onlineAgo is negative).
	rewind := func(seenAgo, onlineAgo time.Duration) time.Time {
		t.Helper()
		var online *time.Time
		if err := d.Pool().QueryRow(ctx, `
			UPDATE host_agents SET last_seen = now() - make_interval(secs => $2::float8),
			  online_since = CASE WHEN $3::float8 < 0 THEN NULL ELSE now() - make_interval(secs => $3::float8) END
			WHERE host_id = $1 RETURNING online_since`,
			h.ID, seenAgo.Seconds(), onlineAgo.Seconds(),
		).Scan(&online); err != nil {
			t.Fatal(err)
		}
		if online == nil {
			return time.Time{}
		}
		return *online
	}
	touch := func(serial string) {
		t.Helper()
		if err := svc.Touch(ctx, serial, "v1"); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat := func() {
		t.Helper()
		if err := svc.Heartbeat(ctx, "9401", agents.HeartbeatReport{Version: "v1", SecretsState: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	startsNow := func(step string) {
		t.Helper()
		o, s, _ := read()
		if o == nil || s == nil || !o.Equal(*s) {
			t.Fatalf("%s: online_since = %v, last_seen = %v; want the period started by this touch", step, o, s)
		}
	}
	kept := func(step string, want time.Time) {
		t.Helper()
		if o, _, _ := read(); o == nil || !o.Equal(want) {
			t.Fatalf("%s: online_since = %v, want it kept at %v", step, o, want)
		}
	}
	within := agents.StaleAfter - 10*time.Second
	beyond := agents.StaleAfter + 10*time.Second

	if o, _, _ := read(); o != nil {
		t.Fatalf("enrolled, never seen: online_since = %v, want NULL", o)
	}
	touch("9401")
	startsNow("first touch")

	start := rewind(within, time.Hour)
	touch("9401")
	kept("touch within the online window", start)
	start = rewind(within, 2*time.Hour)
	heartbeat()
	kept("heartbeat within the online window", start)

	rewind(beyond, time.Hour)
	touch("9401")
	startsNow("touch after a gap longer than the online window")
	rewind(beyond, time.Hour)
	heartbeat()
	startsNow("heartbeat after a gap longer than the online window")

	// An overnight power-off starts a new online period; the connection
	// start (the item expiry's, 24 h gap) is not touched by it.
	_, _, connBefore := read()
	rewind(8*time.Hour, 10*24*time.Hour)
	heartbeat()
	startsNow("back from an 8 h power-off")
	if _, _, c := read(); c == nil || connBefore == nil || !c.Equal(*connBefore) {
		t.Fatalf("connected_since = %v, want it kept at %v across an 8 h power-off", c, connBefore)
	}

	// The upgrade: the column arrives NULL on a row whose agent is online.
	rewind(time.Second, -1)
	touch("9401")
	startsNow("first touch after the upgrade")
	rewind(time.Second, -1)
	heartbeat()
	startsNow("first heartbeat after the upgrade")

	// A registration starts over: NULL until the first touch under the
	// new serial, which starts a new period.
	rewind(time.Second, time.Hour)
	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9402"), "9401"); err != nil {
		t.Fatal(err)
	}
	if o, _, _ := read(); o != nil {
		t.Fatalf("after a renewal: online_since = %v, want NULL", o)
	}
	touch("9402")
	startsNow("first touch after a renewal")
}
