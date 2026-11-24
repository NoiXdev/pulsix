package agents_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestOnlineSinceTracksTheOnlinePeriod walks one agent through the start
// of its current online period (host_agents.online_since, backlog 3.57),
// which a client host's staleness is measured from: NULL after
// enrollment, set by the first touch, kept by every touch or heartbeat
// that follows the previous one within the agents list's online window
// (agents.OnlineWindow: 90 s for an agent that never reported its
// interval), set again after a longer gap — an overnight power-off starts
// a new period, unlike connected_since — set by the first touch after the
// upgrade, and NULL after a registration (renewal or re-enrollment) until
// the first touch under the new serial sets it.
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
	within := agents.OnlineWindowFloor - 10*time.Second
	beyond := agents.OnlineWindowFloor + 10*time.Second

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

// TestOnlineWindowFollowsTheAgentsHeartbeatInterval (backlog 3.58): the
// stored interval (host_agents.heartbeat_interval_s) widens the online
// window in SQL exactly as agents.OnlineWindow does in Go. An agent that
// reports 120 s keeps its online period across a 300 s gap and starts a
// new one after 370 s; the Agents page and the probe candidates read it
// online 300 s after its last contact. Reporting nothing again (a
// downgrade to an agent older than the field) stores NULL, which is the
// 90 s window again.
func TestOnlineWindowFollowsTheAgentsHeartbeatInterval(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-online-window")
	h := createHost(t, d, org, zone, "nb-2")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-b", "v1", "9501")); err != nil {
		t.Fatal(err)
	}
	stored := func() *int32 {
		t.Helper()
		var s *int32
		if err := d.Pool().QueryRow(ctx,
			`SELECT heartbeat_interval_s FROM host_agents WHERE host_id = $1`, h.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := stored(); s != nil {
		t.Fatalf("enrolled: heartbeat_interval_s = %v, want NULL", *s)
	}
	if err := svc.RecordHeartbeatInterval(ctx, "9501", 120); err != nil {
		t.Fatal(err)
	}
	if s := stored(); s == nil || *s != 120 {
		t.Fatalf("after reporting 120 s: heartbeat_interval_s = %v, want 120", s)
	}
	if err := svc.Touch(ctx, "9501", "v1"); err != nil {
		t.Fatal(err)
	}
	// rewind moves last_seen back by seenAgo and online_since an hour.
	rewind := func(seenAgo time.Duration) time.Time {
		t.Helper()
		var online time.Time
		if err := d.Pool().QueryRow(ctx, `
			UPDATE host_agents SET last_seen = now() - make_interval(secs => $2::float8),
			  online_since = now() - interval '1 hour'
			WHERE host_id = $1 RETURNING online_since`, h.ID, seenAgo.Seconds()).Scan(&online); err != nil {
			t.Fatal(err)
		}
		return online
	}
	onlineSince := func() time.Time {
		t.Helper()
		var o time.Time
		if err := d.Pool().QueryRow(ctx, `SELECT online_since FROM host_agents WHERE host_id = $1`, h.ID).Scan(&o); err != nil {
			t.Fatal(err)
		}
		return o
	}

	rewind(300 * time.Second)
	scoped := db.WithOrgScope(ctx, org)
	v, err := svc.ViewByHost(scoped, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Online || v.HeartbeatInterval == nil || *v.HeartbeatInterval != 2*time.Minute {
		t.Fatalf("300 s after the last contact of a 120 s agent: online = %v, interval = %v; want online, 2m",
			v.Online, v.HeartbeatInterval)
	}
	if err := svc.SetProbesSupported(ctx, h.ID, true); err != nil {
		t.Fatal(err)
	}
	if ids, err := svc.OnlineProbeAgents(scoped, org); err != nil || len(ids) != 1 || ids[0] != h.ID {
		t.Fatalf("probe candidates 300 s after the last contact = %v, %v; want the 120 s agent", ids, err)
	}

	start := rewind(300 * time.Second)
	if err := svc.Touch(ctx, "9501", "v1"); err != nil {
		t.Fatal(err)
	}
	if o := onlineSince(); !o.Equal(start) {
		t.Fatalf("touch 300 s after the last one, 120 s interval: online_since = %v, want it kept at %v", o, start)
	}
	start = rewind(300 * time.Second)
	if err := svc.Heartbeat(ctx, "9501", agents.HeartbeatReport{Version: "v1", SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	if o := onlineSince(); !o.Equal(start) {
		t.Fatalf("heartbeat 300 s after the last one, 120 s interval: online_since = %v, want it kept at %v", o, start)
	}

	start = rewind(370 * time.Second)
	if v, err := svc.ViewByHost(scoped, org, h.ID); err != nil || v.Online {
		t.Fatalf("370 s after the last contact of a 120 s agent: online = %v, %v; want offline", v.Online, err)
	}
	if ids, err := svc.OnlineProbeAgents(scoped, org); err != nil || len(ids) != 0 {
		t.Fatalf("probe candidates 370 s after the last contact = %v, %v; want none", ids, err)
	}
	if err := svc.Touch(ctx, "9501", "v1"); err != nil {
		t.Fatal(err)
	}
	if o := onlineSince(); o.Equal(start) {
		t.Fatal("touch 370 s after the last one, 120 s interval: online_since kept, want a new period")
	}

	// A downgrade: the agent reports nothing, the window is 90 s again.
	if err := svc.RecordHeartbeatInterval(ctx, "9501", 0); err != nil {
		t.Fatal(err)
	}
	if s := stored(); s != nil {
		t.Fatalf("after reporting nothing: heartbeat_interval_s = %v, want NULL", *s)
	}
	start = rewind(100 * time.Second)
	if v, err := svc.ViewByHost(scoped, org, h.ID); err != nil || v.Online {
		t.Fatalf("100 s after the last contact without an interval: online = %v, %v; want offline", v.Online, err)
	}
	if err := svc.Touch(ctx, "9501", "v1"); err != nil {
		t.Fatal(err)
	}
	if o := onlineSince(); o.Equal(start) {
		t.Fatal("touch 100 s after the last one without an interval: online_since kept, want a new period")
	}
}

// TestOnlinePeriodRestartsWithinTheBufferWindow (backlog 3.58, review I1):
// an agent whose online window is longer than its disconnect buffer
// (agents.DisconnectBufferWindow, 15 min) still starts a new online period
// after a gap the buffer could not hold — its replay has lost the head of
// the gap, so the 3.118 replay hold must arm. The online verdict keeps the
// agent's full window. A 600 s agent (a 30 min window) keeps its period
// across a 10 min gap and starts a new one after 20 min, by touch and by
// heartbeat alike; a default agent is unchanged (TestOnlineSinceTracksTheOnlinePeriod).
func TestOnlinePeriodRestartsWithinTheBufferWindow(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-online-buffer")
	h := createHost(t, d, org, zone, "nb-3")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-c", "v1", "9601")); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordHeartbeatInterval(ctx, "9601", 600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Touch(ctx, "9601", "v1"); err != nil {
		t.Fatal(err)
	}
	// rewind moves last_seen back by seenAgo and online_since an hour; it
	// returns both as stored.
	rewind := func(seenAgo time.Duration) (seen, online time.Time) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, `
			UPDATE host_agents SET last_seen = now() - make_interval(secs => $2::float8),
			  online_since = now() - interval '1 hour'
			WHERE host_id = $1 RETURNING last_seen, online_since`, h.ID, seenAgo.Seconds()).Scan(&seen, &online); err != nil {
			t.Fatal(err)
		}
		return seen, online
	}
	onlineSince := func() time.Time {
		t.Helper()
		var o time.Time
		if err := d.Pool().QueryRow(ctx, `SELECT online_since FROM host_agents WHERE host_id = $1`, h.ID).Scan(&o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	scoped := db.WithOrgScope(ctx, org)
	for _, write := range []struct {
		name string
		do   func() error
	}{
		{"touch", func() error { return svc.Touch(ctx, "9601", "v1") }},
		{"heartbeat", func() error {
			return svc.Heartbeat(ctx, "9601", agents.HeartbeatReport{Version: "v1", SecretsState: "ok"})
		}},
	} {
		_, start := rewind(10 * time.Minute)
		if v, err := svc.ViewByHost(scoped, org, h.ID); err != nil || !v.Online {
			t.Fatalf("10 min after the last contact of a 600 s agent: online = %v, %v; want online", v.Online, err)
		}
		if err := write.do(); err != nil {
			t.Fatal(err)
		}
		if o := onlineSince(); !o.Equal(start) {
			t.Fatalf("%s 10 min after the last one, 600 s interval: online_since = %v, want it kept at %v", write.name, o, start)
		}

		lastReading, start := rewind(20 * time.Minute)
		if v, err := svc.ViewByHost(scoped, org, h.ID); err != nil || !v.Online {
			t.Fatalf("20 min after the last contact of a 600 s agent: online = %v, %v; want online (the verdict keeps the 30 min window)",
				v.Online, err)
		}
		if err := write.do(); err != nil {
			t.Fatal(err)
		}
		since := onlineSince()
		if since.Equal(start) || !since.After(lastReading) {
			t.Fatalf("%s 20 min after the last one, 600 s interval: online_since = %v, want a new period after %v",
				write.name, since, lastReading)
		}
		// The new period is what arms the replay hold: the host's newest
		// reading predates it until the replay is through.
		if !rules.HostReplaying(since, lastReading, since.Add(time.Second)) {
			t.Fatalf("%s after a 20 min gap: the replay hold is off, want it armed", write.name)
		}
	}
}
