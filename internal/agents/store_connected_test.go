package agents_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
)

// TestConnectedSinceTracksTheConnection walks one agent through the
// connection start the item expiry reads (backlog 3.12c): NULL after
// enrollment, set by the first touch, kept by every touch that follows the
// previous one within the 24-hour reconnect gap — a host powered off for
// 8 hours every night never resets it — set again after a longer gap, set
// by the first touch after the upgrade on a row that has a recent
// last_seen but no start yet, kept across a certificate renewal (and the
// touch after it) within the gap, and NULL after a re-enrollment that
// follows a longer gap, until the first touch sets it. Touch (stream
// open, ingest pacing) and Heartbeat both write it.
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
	// silentFor moves last_seen back by ago and leaves connected_since.
	silentFor := func(ago time.Duration) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx,
			`UPDATE host_agents SET last_seen = now() - make_interval(secs => $2::float8) WHERE host_id = $1`,
			h.ID, ago.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	touch := func() {
		t.Helper()
		if err := svc.Touch(ctx, "9301", "v1"); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat := func() {
		t.Helper()
		if err := svc.Heartbeat(ctx, "9301", agents.HeartbeatReport{Version: "v1", SecretsState: "ok"}); err != nil {
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

	start := rewind(23*time.Hour, 50*time.Hour)
	touch()
	kept("touch 23 h after the last one", start)
	start = rewind(23*time.Hour, 60*time.Hour)
	heartbeat()
	kept("heartbeat 23 h after the last one", start)

	rewind(25*time.Hour, 50*time.Hour)
	touch()
	startsNow("touch after a 25 h gap")
	rewind(25*time.Hour, 50*time.Hour)
	heartbeat()
	startsNow("heartbeat after a 25 h gap")

	// A host powered off for 8 hours every night stays one connection.
	start = rewind(time.Minute, 10*24*time.Hour)
	for night := range 4 {
		silentFor(8 * time.Hour)
		if night%2 == 0 {
			touch()
		} else {
			heartbeat()
		}
		kept("back from a nightly 8 h power-off", start)
	}

	// The upgrade: the column arrives NULL on a row whose agent is online.
	rewind(time.Minute, -1)
	heartbeat()
	startsNow("first heartbeat after the upgrade")
	rewind(time.Minute, -1)
	touch()
	startsNow("first touch after the upgrade")

	// A certificate renewal of a connected agent (every ~60 days) keeps
	// the start, and so does the first touch under the new serial, which
	// follows a last_seen the renewal reset to NULL.
	start = rewind(time.Minute, 10*24*time.Hour)
	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9302"), "9301"); err != nil {
		t.Fatal(err)
	}
	if c, s := read(); s != nil || c == nil || !c.Equal(start) {
		t.Fatalf("after a renewal: connected_since = %v, last_seen = %v; want the start %v kept, last_seen NULL", c, s, start)
	}
	if err := svc.Touch(ctx, "9302", "v1"); err != nil {
		t.Fatal(err)
	}
	kept("first touch after a renewal", start)

	// A re-enrollment after a longer gap is not connected yet.
	rewind(25*time.Hour, 10*24*time.Hour)
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v2", "9303")); err != nil {
		t.Fatal(err)
	}
	if c, s := read(); c != nil || s != nil {
		t.Fatalf("after a re-enrollment 25 h later: connected_since = %v, last_seen = %v; want both NULL", c, s)
	}
	if err := svc.Touch(ctx, "9303", "v2"); err != nil {
		t.Fatal(err)
	}
	startsNow("first touch after re-enrollment")
}
