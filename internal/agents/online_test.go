package agents_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
)

func ptrDuration(d time.Duration) *time.Duration { return &d }

// TestOnlineWindow (backlog 3.58): the online window is three of the
// agent's own heartbeat intervals, never shorter than the 90 s every agent
// had before the interval was reported. An agent that never reported its
// interval (older than the field) keeps exactly that 90 s.
func TestOnlineWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval *time.Duration
		want     time.Duration
	}{
		{"not reported", nil, 90 * time.Second},
		{"the agent's minimum", ptrDuration(5 * time.Second), 90 * time.Second},
		{"the default", ptrDuration(30 * time.Second), 90 * time.Second},
		{"just above the default", ptrDuration(31 * time.Second), 93 * time.Second},
		{"two minutes", ptrDuration(2 * time.Minute), 6 * time.Minute},
		{"the agent's maximum", ptrDuration(10 * time.Minute), 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := agents.OnlineWindow(tc.interval); got != tc.want {
				t.Fatalf("OnlineWindow(%v) = %v, want %v", tc.interval, got, tc.want)
			}
		})
	}
	if agents.OnlineWindowFloor != 90*time.Second {
		t.Fatalf("OnlineWindowFloor = %v, want today's 90 s", agents.OnlineWindowFloor)
	}
}

// TestListViewUsesTheAgentsOwnWindow (backlog 3.58): an agent that
// reports a 120 s heartbeat interval stays online 300 s after its last
// contact and goes offline after 360 s; one that never reported its
// interval goes offline after 90 s, as before.
func TestListViewUsesTheAgentsOwnWindow(t *testing.T) {
	svc, st, _, now := newService(t)
	org := uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		name     string
		interval int32
		seenAgo  time.Duration
		online   bool
	}{
		{"slow-quiet", 120, 300 * time.Second, true},
		{"slow-gone", 120, 361 * time.Second, false},
		{"old-recent", 0, 89 * time.Second, true},
		{"old-gone", 0, 91 * time.Second, false},
	} {
		host := uuid.Must(uuid.NewV7())
		serial := "serial-" + tc.name
		if err := svc.Register(ctx, reg(org, host, tc.name, "v1", serial)); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordHeartbeatInterval(ctx, serial, tc.interval); err != nil {
			t.Fatal(err)
		}
		st.setLastSeen(host, ptrTime(now.Add(-tc.seenAgo)))
		v, err := svc.ViewByHost(ctx, org, host)
		if err != nil {
			t.Fatal(err)
		}
		if v.Online != tc.online {
			t.Errorf("%s: last seen %v ago with interval %d s: online = %v, want %v",
				tc.name, tc.seenAgo, tc.interval, v.Online, tc.online)
		}
	}
	views, err := svc.ListView(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"slow-quiet": true, "slow-gone": false, "old-recent": true, "old-gone": false}
	for _, v := range views {
		if v.Online != want[v.AgentName] {
			t.Errorf("ListView %s: online = %v, want %v", v.AgentName, v.Online, want[v.AgentName])
		}
	}
}

// TestRecordHeartbeatIntervalBoundsTheReport: the agent's report is
// stored as sent within the agent's own bounds; nothing (0, an agent
// older than the field) or nonsense stores "not reported", which is the
// 90 s window, and anything over the agent's maximum (10 min) is stored as
// that maximum, so a lying agent cannot keep itself online for days.
func TestRecordHeartbeatIntervalBoundsTheReport(t *testing.T) {
	svc, st, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "a", "v1", "serial-a")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		seconds int32
		want    *time.Duration
	}{
		{120, ptrDuration(2 * time.Minute)},
		{0, nil},
		{5, ptrDuration(5 * time.Second)},
		{-30, nil},
		{86400, ptrDuration(10 * time.Minute)},
	} {
		if err := svc.RecordHeartbeatInterval(ctx, "serial-a", tc.seconds); err != nil {
			t.Fatal(err)
		}
		got := st.heartbeatInterval(host)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("reported %d s: stored %v, want %v", tc.seconds, got, tc.want)
		}
	}
}
