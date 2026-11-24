package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// testAgentWindow is the online window rule these tests build the store
// with. Production passes agents.OnlineWindowFloor and
// agents.HeartbeatsPerOnlineWindow (internal/app pins that); the values
// here only have to be ones the timestamps below are clearly inside or
// outside of. testFloor is the window of an agent that never reported its
// heartbeat interval.
const testFloor = 90 * time.Second

var testAgentWindow = rules.AgentOnlineWindow{Floor: testFloor, HeartbeatsPerWindow: 3}

// agentRow is a host_agents row written relative to the database's own
// now(): lastSeen and online are SQL interval texts ("10 seconds"), or
// "" for NULL. intervalS is the reported heartbeat interval in seconds, 0
// for NULL (never reported). The timestamps actually stored are returned,
// so a test compares against what the database holds rather than a Go
// clock.
type agentRow struct {
	lastSeen, online string
	intervalS        int32
}

func insertAgent(t *testing.T, d *db.DB, org, host uuid.UUID, a agentRow) (lastSeen, online, created *time.Time) {
	t.Helper()
	ago := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var interval *int32
	if a.intervalS > 0 {
		interval = &a.intervalS
	}
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, last_seen, online_since, connected_since,
		                         heartbeat_interval_s)
		VALUES ($1, $2, 'agent', $3,
		        now() - $4::text::interval, now() - $5::text::interval, now() - interval '30 days', $6)
		RETURNING last_seen, online_since, created_at`,
		host, org, uuid.NewString(), ago(a.lastSeen), ago(a.online), interval).Scan(&lastSeen, &online, &created)
	if err != nil {
		t.Fatal(err)
	}
	return lastSeen, online, created
}

func setKind(t *testing.T, d *db.DB, host uuid.UUID, kind string) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(), `UPDATE hosts SET kind = $2 WHERE id = $1`, host, kind); err != nil {
		t.Fatal(err)
	}
}

func setAbsentDays(t *testing.T, d *db.DB, org uuid.UUID, days int) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO org_monitoring_settings (org_id, client_absent_days) VALUES ($1, $2)`, org, days); err != nil {
		t.Fatal(err)
	}
}

func loadPresence(t *testing.T, d *db.DB, org, host uuid.UUID) rules.HostPresence {
	t.Helper()
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), testAgentWindow, discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host}, time.Now())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return load.Presence
}

func timeOf(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// TestLoadFillsTheHostsPresence (backlog 3.57): Load reads the host's
// kind, whether it has an agent, the agents list's own online verdict
// (last_seen within the window the store was built with), the agent's
// last contact and the start of its online period, and — for a client with an agent —
// the organization's absent threshold with the default applied and the
// online window the store was built with (the unknown alert's grace).
func TestLoadFillsTheHostsPresence(t *testing.T) {
	d := testdb.Open(t)

	t.Run("a server without an agent is the zero presence", func(t *testing.T) {
		org := seedOrg(t, d, "presence-server")
		host := seedHost(t, d, org, "h")
		if got := loadPresence(t, d, org, host); got != (rules.HostPresence{}) {
			t.Fatalf("presence = %+v, want the zero value", got)
		}
	})

	t.Run("a server's agent is read but no threshold is", func(t *testing.T) {
		org := seedOrg(t, d, "presence-server-agent")
		host := seedHost(t, d, org, "h")
		seen, on, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "10 seconds", online: "1 hour"})
		want := rules.HostPresence{HasAgent: true, AgentOnline: true, AgentLastSeen: timeOf(seen), AgentOnlineSince: timeOf(on)}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v", got, want)
		}
	})

	t.Run("an online client with its organization's threshold", func(t *testing.T) {
		org := seedOrg(t, d, "presence-online")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		setAbsentDays(t, d, org, 7)
		seen, on, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "10 seconds", online: "1 hour"})
		want := rules.HostPresence{
			Client: true, HasAgent: true, AgentOnline: true,
			AgentLastSeen: timeOf(seen), AgentOnlineSince: timeOf(on), AbsentAfter: 7 * 24 * time.Hour,
			OnlineWindow: testFloor,
		}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v", got, want)
		}
	})

	t.Run("a client whose agent is past the window is not online; no row is the default threshold", func(t *testing.T) {
		org := seedOrg(t, d, "presence-offline")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		seen, on, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "100 seconds", online: "3 days"})
		want := rules.HostPresence{
			Client: true, HasAgent: true, AgentOnline: false,
			AgentLastSeen: timeOf(seen), AgentOnlineSince: timeOf(on), AbsentAfter: 14 * 24 * time.Hour,
			OnlineWindow: testFloor,
		}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v", got, want)
		}
	})

	t.Run("an agent that never touched is not online, was last seen at its registration and has no online start", func(t *testing.T) {
		org := seedOrg(t, d, "presence-never")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		setAbsentDays(t, d, org, 0)
		_, _, created := insertAgent(t, d, org, host, agentRow{})
		want := rules.HostPresence{Client: true, HasAgent: true, AgentLastSeen: timeOf(created), OnlineWindow: testFloor}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v (the warning off, the online start unknown)", got, want)
		}
	})

	// Backlog 3.58: the window is the agent's own, three of its reported
	// heartbeat intervals and never less than the floor — the online
	// verdict and the unknown grace alike.
	t.Run("a client whose agent reports 120 s is online 300 s after its last contact", func(t *testing.T) {
		org := seedOrg(t, d, "presence-slow-online")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		seen, on, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "300 seconds", online: "1 hour", intervalS: 120})
		want := rules.HostPresence{
			Client: true, HasAgent: true, AgentOnline: true,
			AgentLastSeen: timeOf(seen), AgentOnlineSince: timeOf(on), AbsentAfter: 14 * 24 * time.Hour,
			OnlineWindow: 6 * time.Minute,
		}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v", got, want)
		}
	})

	t.Run("a client whose agent reports 120 s is offline 370 s after its last contact", func(t *testing.T) {
		org := seedOrg(t, d, "presence-slow-offline")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		seen, on, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "370 seconds", online: "1 hour", intervalS: 120})
		want := rules.HostPresence{
			Client: true, HasAgent: true, AgentOnline: false,
			AgentLastSeen: timeOf(seen), AgentOnlineSince: timeOf(on), AbsentAfter: 14 * 24 * time.Hour,
			OnlineWindow: 6 * time.Minute,
		}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v", got, want)
		}
	})

	t.Run("a reported interval under the floor keeps the floor", func(t *testing.T) {
		org := seedOrg(t, d, "presence-fast")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		seen, on, _ := insertAgent(t, d, org, host, agentRow{lastSeen: "80 seconds", online: "1 hour", intervalS: 10})
		want := rules.HostPresence{
			Client: true, HasAgent: true, AgentOnline: true,
			AgentLastSeen: timeOf(seen), AgentOnlineSince: timeOf(on), AbsentAfter: 14 * 24 * time.Hour,
			OnlineWindow: testFloor,
		}
		if got := loadPresence(t, d, org, host); !equalPresence(got, want) {
			t.Fatalf("presence = %+v, want %+v", got, want)
		}
	})

	t.Run("a client without an agent", func(t *testing.T) {
		org := seedOrg(t, d, "presence-agentless")
		host := seedHost(t, d, org, "h")
		setKind(t, d, host, "client")
		if got := loadPresence(t, d, org, host); got != (rules.HostPresence{Client: true}) {
			t.Fatalf("presence = %+v, want only Client", got)
		}
	})
}

func equalPresence(a, b rules.HostPresence) bool {
	return a.Client == b.Client && a.HasAgent == b.HasAgent && a.AgentOnline == b.AgentOnline &&
		a.AgentLastSeen.Equal(b.AgentLastSeen) && a.AgentOnlineSince.Equal(b.AgentOnlineSince) &&
		a.AbsentAfter == b.AbsentAfter && a.OnlineWindow == b.OnlineWindow
}

// TestApplyOfAFrozenResultWritesOnlyTheCursor: a frozen evaluation keeps
// every state row and the roll-up exactly as they are — not rewritten
// with a new evaluated_at, and in particular not deleted, although a
// frozen Result for a host with no previous roll-up carries the zero Host
// that would otherwise mean "delete". Only the cursor advances.
func TestApplyOfAFrozenResultWritesOnlyTheCursor(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "frozen-apply")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), testAgentWindow, discard())
	now := time.Now().UTC().Truncate(time.Microsecond)
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCheckState(t, d, org, host, stateRow{tuple: cpu, state: rules.StateCrit, since: now.Add(-2 * time.Hour), evaluatedAt: now.Add(-time.Hour)})
	insertHostState(t, d, org, host, rules.StateCrit, 1, 0, 0, now.Add(-2*time.Hour), now.Add(-time.Hour))
	markDirty(t, d, org, host)

	res := rules.Result{Frozen: true, NextDue: now.Add(90 * time.Second)}
	if _, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, res)
	}); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].State != rules.StateCrit || !states[0].EvaluatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("states = %+v, want the stored crit untouched", states)
	}
	hostStates, err := rules.NewSQLStore(d).HostStates(context.Background(), org, rules.FleetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hostStates) != 1 || hostStates[0].State != rules.StateCrit || !hostStates[0].EvaluatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("roll-up = %+v, want the stored crit untouched", hostStates)
	}
	if _, _, nextDue := cursorRow(t, d, host); !nextDue.Equal(res.NextDue) {
		t.Fatalf("next_due_at = %v, want %v", nextDue, res.NextDue)
	}
	if got := claimAll(t, store, 10); len(got) != 0 {
		t.Fatalf("the frozen host was claimed again: %+v", got)
	}
}

// TestNewEvalSQLStoreRefusesNoAgentWindow: a zero floor would read every
// agent that never reported its interval as offline and freeze its client
// for ever, quietly, and no heartbeats per window would ignore every
// reported interval. Refused at construction, like a nil ItemStore.
func TestNewEvalSQLStoreRefusesNoAgentWindow(t *testing.T) {
	for _, w := range []rules.AgentOnlineWindow{
		{},
		{HeartbeatsPerWindow: 3},
		{Floor: testFloor},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewEvalSQLStore(%+v) returned a store, want a panic at construction", w)
				}
			}()
			rules.NewEvalSQLStore(nil, cpuMinute(), &fakeItemStore{}, w, discard())
		}()
	}
}
