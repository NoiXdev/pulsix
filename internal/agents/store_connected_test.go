package agents_test

import (
	"testing"
	"time"
)

// TestConnectedSinceTracksTheConnection walks one agent through the
// connection start the item expiry reads (backlog 3.12c): NULL after
// enrollment, set by the first touch, kept by every touch that follows the
// previous one within the 30-minute reconnect gap, set again after a
// longer gap, set by the first touch after the upgrade on a row that has a
// recent last_seen but no start yet, and NULL again after a
// re-enrollment. Touch (stream open, ingest pacing) and Heartbeat both
// write it.
func TestConnectedSinceTracksTheConnection(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-connected")
	h := createHost(t, d, org, zone, "db-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9301")); err != nil {
		t.Fatal(err)
	}
	// read returns connected_since and last_seen (nil when NULL).
	read := func() (connected, seen *time.Time) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx,
			`SELECT connected_since, last_seen FROM host_agents WHERE host_id = $1`, h.ID,
		).Scan(&connected, &seen); err != nil {
			t.Fatal(err)
		}
		return connected, seen
	}
	// rewind moves last_seen back by seenAgo and sets connected_since to
	// connectedAgo before now (NULL when connectedAgo is negative).
	rewind := func(seenAgo, connectedAgo time.Duration) time.Time {
		t.Helper()
		var connected *time.Time
		if err := d.Pool().QueryRow(ctx, `
			UPDATE host_agents SET last_seen = now() - make_interval(secs => $2::float8),
			  connected_since = CASE WHEN $3::float8 < 0 THEN NULL ELSE now() - make_interval(secs => $3::float8) END
			WHERE host_id = $1 RETURNING connected_since`,
			h.ID, seenAgo.Seconds(), connectedAgo.Seconds(),
		).Scan(&connected); err != nil {
			t.Fatal(err)
		}
		if connected == nil {
			return time.Time{}
		}
		return *connected
	}
	touch := func() {
		t.Helper()
		if err := svc.Touch(ctx, "9301", "v1"); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat := func() {
		t.Helper()
		if err := svc.Heartbeat(ctx, "9301", "v1", nil, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	// startsNow: the connection start is this write's own last_seen.
	startsNow := func(step string) {
		t.Helper()
		c, s := read()
		if c == nil || s == nil || !c.Equal(*s) {
			t.Fatalf("%s: connected_since = %v, last_seen = %v; want the start set to this touch", step, c, s)
		}
	}
	// kept: the connection start is still want.
	kept := func(step string, want time.Time) {
		t.Helper()
		if c, _ := read(); c == nil || !c.Equal(want) {
			t.Fatalf("%s: connected_since = %v, want it kept at %v", step, c, want)
		}
	}

	if c, s := read(); c != nil || s != nil {
		t.Fatalf("enrolled, never seen: connected_since = %v, last_seen = %v; want both NULL", c, s)
	}

	touch()
	startsNow("first touch")

	start := rewind(29*time.Minute, 2*time.Hour)
	touch()
	kept("touch 29 min after the last one", start)
	start = rewind(29*time.Minute, 3*time.Hour)
	heartbeat()
	kept("heartbeat 29 min after the last one", start)

	rewind(31*time.Minute, 5*time.Hour)
	touch()
	startsNow("touch after a 31 min gap")
	rewind(31*time.Minute, 5*time.Hour)
	heartbeat()
	startsNow("heartbeat after a 31 min gap")

	// The upgrade: the column arrives NULL on a row whose agent is online.
	rewind(time.Minute, -1)
	heartbeat()
	startsNow("first heartbeat after the upgrade")
	rewind(time.Minute, -1)
	touch()
	startsNow("first touch after the upgrade")

	// A re-enrollment is a new agent: not connected yet.
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v2", "9302")); err != nil {
		t.Fatal(err)
	}
	if c, s := read(); c != nil || s != nil {
		t.Fatalf("after re-enrollment: connected_since = %v, last_seen = %v; want both NULL", c, s)
	}
	if err := svc.Touch(ctx, "9302", "v2"); err != nil {
		t.Fatal(err)
	}
	startsNow("first touch after re-enrollment")
}
