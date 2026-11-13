// The automatic expiry's "the host kept reporting" half against the real
// database, with the manager's own probe results in the picture (backlog
// 3.12b, final review). Probe results land in check_current under the
// host they check, so a host's newest reading stays fresh while its agent
// is offline; that alone must not make the agent's items look gone. With
// T = the item's newest reading + the period, the host counts as having
// kept reporting after T when its item's own check key has a reading after
// T, when it has an agent last seen after T that has been connected for
// checks.MaxInterval by then (backlog 3.12c), or — only when it has no
// agent — when any of its readings is after T.
//
// Each scenario is pinned twice: on the selection (ListExpiredItems) and,
// through a store that hands every item to the service, on the re-check
// inside each forget transaction alone (ItemNewest + itemExpired) — the
// two must agree.
package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// putAgent gives host an agent last seen at lastSeen (never, when nil),
// connected since connected (unknown, when nil), and returns its serial.
func putAgent(t *testing.T, d *db.DB, org, host uuid.UUID, lastSeen, connected *time.Time) string {
	t.Helper()
	serial := uuid.NewString()
	if _, err := gen.New(d.Pool()).UpsertHostAgent(ctx, gen.UpsertHostAgentParams{
		HostID: host, OrgID: org, AgentName: "agent-" + host.String()[:8], CertSerial: serial, Version: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE host_agents SET last_seen = $2, connected_since = $3 WHERE host_id = $1`,
		host, lastSeen, connected); err != nil {
		t.Fatal(err)
	}
	return serial
}

// everyItemStore hands the service every item of its hosts as expired, so
// ForgetExpired's outcome is the in-transaction re-check's alone.
type everyItemStore struct {
	*checks.SQLStore
	d     *db.DB
	hosts []uuid.UUID
}

func (s everyItemStore) ExpiredItems(context.Context, time.Time, time.Duration, int) ([]checks.ItemRef, error) {
	rows, err := s.d.Pool().Query(ctx,
		`SELECT DISTINCT org_id, host_id, check_key, item FROM check_current WHERE host_id = ANY($1)`, s.hosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []checks.ItemRef
	for rows.Next() {
		var r checks.ItemRef
		if err := rows.Scan(&r.OrgID, &r.HostID, &r.CheckKey, &r.Item); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TestExpiryWithAgentsAndProbes is the final review's ruling, one host per
// scenario.
func TestExpiryWithAgentsAndProbes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	period := 48 * time.Hour
	quiet := now.Add(-period - 2*time.Hour) // newest reading of a gone (or merely silent) item
	recent := now.Add(-time.Minute)
	ago := func(d time.Duration) *time.Time { at := now.Add(-d); return &at }
	steady := ago(60 * 24 * time.Hour) // connected long before any of this
	seen := now.Add(-30 * time.Second) // an online agent's last heartbeat
	before := func(at time.Time, d time.Duration) *time.Time { b := at.Add(-d); return &b }

	type reading struct {
		key, item string
		at        time.Time
	}
	type item struct{ key, item string }
	cases := []struct {
		name      string
		agent     bool
		lastSeen  *time.Time
		connected *time.Time
		readings  []reading
		due       map[item]bool
	}{
		{
			// The agent went offline with everything it reported; the
			// server's own HTTP check on the host kept running.
			name: "offline agent, fresh probe on another check key", agent: true, lastSeen: &quiet, connected: steady,
			readings: []reading{{"disk", "/", quiet}, {"disk", "/data", quiet}, {"cpu", "", quiet}, {"web", "https://x.test", recent}},
			due:      map[item]bool{{"disk", "/"}: false, {"disk", "/data"}: false, {"cpu", ""}: false, {"web", "https://x.test"}: false},
		},
		{
			name: "agent never seen, fresh probe", agent: true, lastSeen: nil,
			readings: []reading{{"disk", "/", quiet}, {"web", "https://x.test", recent}},
			due:      map[item]bool{{"disk", "/"}: false},
		},
		{
			// One mount went away; the others still report.
			name: "online agent, one mount quiet", agent: true, lastSeen: &seen, connected: steady,
			readings: []reading{{"disk", "/old", quiet}, {"disk", "/", recent}, {"cpu", "", recent}},
			due:      map[item]bool{{"disk", "/old"}: true, {"disk", "/"}: false, {"cpu", ""}: false},
		},
		{
			// The check was removed: none of its items report any more, but
			// the agent's heartbeat says the host is alive.
			name: "online agent, whole check key quiet", agent: true, lastSeen: &seen, connected: steady,
			readings: []reading{{"docker", "web", quiet}, {"docker", "db", quiet}},
			due:      map[item]bool{{"docker", "web"}: true, {"docker", "db"}: true},
		},
		{
			// Backlog 3.12c: the agent was offline for longer than the
			// period and has just reconnected; its nightly check has not
			// run again yet. Its heartbeat says nothing about that check.
			name: "agent back from an outage, long-interval check not run yet", agent: true, lastSeen: &seen, connected: ago(5 * time.Minute),
			readings: []reading{{"backup", "nightly", quiet}, {"cpu", "", recent}},
			due:      map[item]bool{{"backup", "nightly"}: false, {"cpu", ""}: false},
		},
		{
			name: "agent connected a second short of MaxInterval", agent: true, lastSeen: &seen, connected: before(seen, checks.MaxInterval-time.Second),
			readings: []reading{{"backup", "nightly", quiet}},
			due:      map[item]bool{{"backup", "nightly"}: false},
		},
		{
			// A day connected: the check has had its chance to report.
			name: "agent connected exactly MaxInterval", agent: true, lastSeen: &seen, connected: before(seen, checks.MaxInterval),
			readings: []reading{{"backup", "nightly", quiet}},
			due:      map[item]bool{{"backup", "nightly"}: true},
		},
		{
			// The upgrade: online, but no connection start recorded yet.
			name: "online agent, connection start unknown", agent: true, lastSeen: &seen,
			readings: []reading{{"backup", "nightly", quiet}},
			due:      map[item]bool{{"backup", "nightly"}: false},
		},
		{
			// The item's own check key still counts while the agent does not.
			name: "agent just reconnected, a sibling mount reports", agent: true, lastSeen: &seen, connected: ago(5 * time.Minute),
			readings: []reading{{"disk", "/old", quiet}, {"disk", "/", recent}},
			due:      map[item]bool{{"disk", "/old"}: true, {"disk", "/"}: false},
		},
		{
			name: "agentless host, one of two synthetic checks removed", agent: false,
			readings: []reading{{"ping", "", quiet}, {"web", "https://x.test", recent}},
			due:      map[item]bool{{"ping", ""}: true, {"web", "https://x.test"}: false},
		},
		{
			name: "agentless host, all quiet", agent: false,
			readings: []reading{{"ping", "", quiet}, {"web", "https://x.test", quiet.Add(time.Hour)}},
			due:      map[item]bool{{"ping", ""}: false, {"web", "https://x.test"}: false},
		},
	}

	// seed writes every scenario on fresh hosts of a fresh organization.
	seed := func(t *testing.T, d *db.DB) (uuid.UUID, []uuid.UUID) {
		org := seedOrg(t, d, "checks-expiry-agents-"+uuid.NewString()[:8])
		hosts := make([]uuid.UUID, len(cases))
		for i, tc := range cases {
			hosts[i] = seedHost(t, d, org, "expiry-agents-"+uuid.NewString()[:8])
			if tc.agent {
				putAgent(t, d, org, hosts[i], tc.lastSeen, tc.connected)
			}
			for _, r := range tc.readings {
				putCurrent(t, d, org, hosts[i], r.key, r.item, "m", r.at)
			}
		}
		return org, hosts
	}

	t.Run("selection", func(t *testing.T) {
		d := testdb.Open(t)
		org, hosts := seed(t, d)
		got := expiredSet(t, d, now.Add(-period), period, 100)
		for i, tc := range cases {
			for it, want := range tc.due {
				ref := checks.ItemRef{OrgID: org, HostID: hosts[i], CheckKey: it.key, Item: it.item}
				if got[ref] != want {
					t.Errorf("%s: %s/%q selected = %v, want %v", tc.name, it.key, it.item, got[ref], want)
				}
			}
		}
	})

	t.Run("re-check", func(t *testing.T) {
		d := testdb.Open(t)
		_, hosts := seed(t, d)
		svc := checks.NewService(everyItemStore{SQLStore: checks.NewSQLStore(d), d: d, hosts: hosts}, fixedClock(now),
			checks.WithAlertCloser(alerts.NewService(alerts.NewSQLStore(d), func() time.Time { return now })))
		if _, err := svc.ForgetExpired(context.Background(), period, 100); err != nil {
			t.Fatalf("forget expired: %v", err)
		}
		for i, tc := range cases {
			for it, due := range tc.due {
				want := 1
				if due {
					want = 0
				}
				if n := itemRows(t, d, "check_current", hosts[i], it.key, it.item); n != want {
					t.Errorf("%s: %s/%q has %d current rows after the re-check, want %d", tc.name, it.key, it.item, n, want)
				}
			}
		}
	})
}

// TestForgetItemProceedSeesTheAgent: the re-check reads the item's check
// key's newest reading and the host's agent next to the item's and the
// host's newest readings.
func TestForgetItemProceedSeesTheAgent(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-proceed-agent")
	host := seedHost(t, d, org, "forget-proceed-agent-host")
	now := time.Now().UTC().Truncate(time.Second)
	seen := now.Add(-time.Hour)
	connected := now.Add(-50 * time.Hour)
	putAgent(t, d, org, host, &seen, &connected)
	putCurrent(t, d, org, host, "disk", "/old", "free", now.Add(-3*time.Hour))
	putCurrent(t, d, org, host, "disk", "/", "free", now.Add(-2*time.Hour))
	putCurrent(t, d, org, host, "web", "", "up", now)

	var got checks.ItemNewest
	if _, err := checks.NewSQLStore(d).ForgetItem(ctx, checks.ItemRef{OrgID: org, HostID: host, CheckKey: "disk", Item: "/old"},
		checks.ForgetSteps{Proceed: func(n checks.ItemNewest) bool { got = n; return false }}); err != nil {
		t.Fatal(err)
	}
	if got.Rows != 1 || !got.Item.Equal(now.Add(-3*time.Hour)) || !got.Key.Equal(now.Add(-2*time.Hour)) ||
		!got.Host.Equal(now) || !got.HasAgent || !got.AgentSeen.Equal(seen) || !got.AgentConnected.Equal(connected) {
		t.Fatalf("Proceed saw %+v", got)
	}

	// An agent never seen reads as having an agent with no heartbeat and
	// no connection start.
	putAgent(t, d, org, host, nil, nil)
	if _, err := checks.NewSQLStore(d).ForgetItem(ctx, checks.ItemRef{OrgID: org, HostID: host, CheckKey: "disk", Item: "/old"},
		checks.ForgetSteps{Proceed: func(n checks.ItemNewest) bool { got = n; return false }}); err != nil {
		t.Fatal(err)
	}
	if !got.HasAgent || got.AgentSeen.After(time.Unix(0, 0)) || got.AgentConnected.After(time.Unix(0, 0)) {
		t.Fatalf("never-seen agent: Proceed saw %+v", got)
	}
}

// TestExpiryAfterAnAgentOutage is backlog 3.12c end to end, with the
// agent's reconnect written by the agents store's own heartbeat: an agent
// offline for longer than the period comes back before its nightly check
// has run again; neither the selection nor the forget's re-check takes
// that check's item for gone. Once the agent has been connected for
// checks.MaxInterval and the item still has not reported, it expires.
func TestExpiryAfterAnAgentOutage(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-expiry-outage")
	host := seedHost(t, d, org, "expiry-outage-host")
	now := time.Now().UTC().Truncate(time.Second)
	period := 48 * time.Hour
	quiet := now.Add(-period - 72*time.Hour) // the outage began here
	steady := quiet.Add(-60 * 24 * time.Hour)
	serial := putAgent(t, d, org, host, &quiet, &steady)
	putCurrent(t, d, org, host, "backup", "nightly", "ok", quiet)
	ref := checks.ItemRef{OrgID: org, HostID: host, CheckKey: "backup", Item: "nightly"}

	// The agent reconnects: the stream's open touches it.
	if err := agents.NewSQLStore(d).Touch(ctx, serial, "test"); err != nil {
		t.Fatal(err)
	}
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now),
		checks.WithAlertCloser(alerts.NewService(alerts.NewSQLStore(d), func() time.Time { return now })))
	if expiredSet(t, d, now.Add(-period), period, 100)[ref] {
		t.Fatal("selected right after the reconnect, want kept until the check has had a day to report")
	}
	if counts, err := svc.ForgetExpired(ctx, period, 100); err != nil || counts[org] != 0 {
		t.Fatalf("ForgetExpired right after the reconnect = %v, %v; want nothing forgotten", counts, err)
	}
	if n := itemRows(t, d, "check_current", host, "backup", "nightly"); n != 1 {
		t.Fatalf("item has %d current rows after the reconnect, want 1", n)
	}

	// A day later, still connected, the item has not come back: gone.
	if _, err := d.Pool().Exec(ctx,
		`UPDATE host_agents SET connected_since = last_seen - make_interval(secs => $2::float8) WHERE host_id = $1`,
		host, checks.MaxInterval.Seconds()); err != nil {
		t.Fatal(err)
	}
	if !expiredSet(t, d, now.Add(-period), period, 100)[ref] {
		t.Fatal("not selected after a day connected, want it due")
	}
	if counts, err := svc.ForgetExpired(ctx, period, 100); err != nil || counts[org] != 1 {
		t.Fatalf("ForgetExpired after a day connected = %v, %v; want the item forgotten", counts, err)
	}
}
