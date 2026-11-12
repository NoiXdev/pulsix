package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

var healthy = serverStats{Connections: 12, MaxConnections: 151, SlowQueries: 100}

func TestIdentity(t *testing.T) {
	p := New()
	if p.Name() != "mysql" {
		t.Fatalf("name %q", p.Name())
	}
	sp, ok := p.(checks.StatefulPlugin)
	if !ok {
		t.Fatal("not a StatefulPlugin")
	}
	inst := sp.NewInstance()
	if _, ok := inst.(checks.SecretPlugin); !ok {
		t.Fatal("the instance is not a SecretPlugin: the scheduler asserts the instance")
	}
	if inst.(checks.SecretPlugin).SecretErrorMetric() != "mysql.up" {
		t.Fatal("SecretErrorMetric is not mysql.up")
	}
	if inst == p {
		t.Fatal("NewInstance returned the prototype")
	}
}

func TestHealthyRound(t *testing.T) {
	var got connSettings
	p := newTestPlugin(&fakeSession{stats: healthy}, nil, &got)
	obs, err := collect(t, p, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	if got.user != "mon" || got.password != plantedPassword || got.host != "127.0.0.1" {
		t.Fatalf("settings %+v", got)
	}
	mustValue(t, obs, "", "mysql.up", 1)
	mustValue(t, obs, "", "mysql.connections", 12)
	mustValue(t, obs, "", "mysql.max_connections", 151)
	conns, maxConns := 12.0, 151.0
	mustValue(t, obs, "", "mysql.connections_pct", conns/maxConns*100)
	mustLabel(t, obs, "", "mysql.replication_state", "none")
	mustValue(t, obs, "", "mysql.replication_lag_seconds", 0)
	if _, ok := find(obs, "", "mysql.slow_queries_per_s"); ok {
		t.Fatal("a rate in the first round")
	}
	for _, o := range obs {
		if o.Item != "" {
			t.Errorf("unexpected item %q", o.Item)
		}
	}
}

func TestSessionIsClosed(t *testing.T) {
	for name, sess := range map[string]*fakeSession{
		"ok":    {stats: healthy},
		"error": {statsErr: errors.New("x")},
	} {
		if _, err := collect(t, newTestPlugin(sess, nil, nil), goodFields); err != nil {
			t.Fatal(err)
		}
		if !sess.closed {
			t.Errorf("%s: session left open", name)
		}
	}
}

func TestMaxConnectionsNotPositive(t *testing.T) {
	st := healthy
	st.MaxConnections = 0
	obs, _ := collect(t, newTestPlugin(&fakeSession{stats: st}, nil, nil), goodFields)
	mustValue(t, obs, "", "mysql.up", 1)
	mustErr(t, obs, "", "mysql.connections_pct", "mysql: max_connections is not positive")
}

func TestSlowQueryRate(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{stats: healthy}
	p := newTestPlugin(sess, nil, nil)
	p.now = c.now

	round := func(slow uint64, advance time.Duration) []checks.Observation {
		t.Helper()
		c.t = c.t.Add(advance)
		sess.stats.SlowQueries = slow
		obs, err := collect(t, p, goodFields)
		if err != nil {
			t.Fatal(err)
		}
		return obs
	}
	if _, ok := find(round(100, 0), "", "mysql.slow_queries_per_s"); ok {
		t.Fatal("first round has a rate")
	}
	mustValue(t, round(130, 10*time.Second), "", "mysql.slow_queries_per_s", 3)
	mustValue(t, round(130, 10*time.Second), "", "mysql.slow_queries_per_s", 0)
	// A restart resets the counter: no rate that round, a new baseline.
	if _, ok := find(round(5, 10*time.Second), "", "mysql.slow_queries_per_s"); ok {
		t.Fatal("a rate after a counter reset")
	}
	mustValue(t, round(25, 10*time.Second), "", "mysql.slow_queries_per_s", 2)
}

// A failed round keeps the baseline: the next success rates across the gap.
func TestSlowQueryRateSurvivesAFailedRound(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{stats: healthy}
	p := newTestPlugin(sess, nil, nil)
	p.now = c.now
	_, _ = collect(t, p, goodFields)
	sess.statsErr = errors.New("x")
	c.t = c.t.Add(10 * time.Second)
	_, _ = collect(t, p, goodFields)
	sess.statsErr = nil
	sess.stats.SlowQueries = 160
	c.t = c.t.Add(10 * time.Second)
	obs, _ := collect(t, p, goodFields)
	mustValue(t, obs, "", "mysql.slow_queries_per_s", 3)
}

// Two instances never share a baseline (the scheduler's per-key contract).
func TestInstancesDoNotShareTheCounter(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	proto := newTestPlugin(&fakeSession{stats: healthy}, nil, nil)
	proto.now = c.now
	// The prototype collects first: instances must not inherit its baseline.
	_, _ = proto.CollectWithSecret(context.Background(), nil, goodFields)
	a := proto.NewInstance().(*plugin)
	b := proto.NewInstance().(*plugin)
	_, _ = a.CollectWithSecret(context.Background(), nil, goodFields)
	c.t = c.t.Add(10 * time.Second)
	obs, _ := b.CollectWithSecret(context.Background(), nil, goodFields)
	if _, ok := find(obs, "", "mysql.slow_queries_per_s"); ok {
		t.Fatal("instance b rated from instance a's baseline")
	}
}

func TestReplicationStates(t *testing.T) {
	tests := []struct {
		name  string
		chs   []replicaChannel
		state string
		lag   *float64 // nil: an error observation
	}{
		{"not a replica", nil, "none", f64(0)},
		{"running", []replicaChannel{{IO: "Yes", SQL: "Yes", Lag: f64(3)}}, "running", f64(3)},
		{"running lower case", []replicaChannel{{IO: "yes", SQL: "YES", Lag: f64(0)}}, "running", f64(0)},
		{"io connecting", []replicaChannel{{IO: "Connecting", SQL: "Yes", Lag: f64(2)}}, "connecting", f64(2)},
		{"io connecting NULL lag", []replicaChannel{{IO: "Connecting", SQL: "Yes"}}, "connecting", nil},
		{"io stopped", []replicaChannel{{IO: "No", SQL: "Yes", Lag: f64(9)}}, "stopped", f64(9)},
		{"sql stopped NULL lag", []replicaChannel{{IO: "Yes", SQL: "No"}}, "stopped", nil},
		{"both stopped NULL lag", []replicaChannel{{IO: "No", SQL: "No"}}, "stopped", nil},
		{"connecting with sql stopped", []replicaChannel{{IO: "Connecting", SQL: "No"}}, "stopped", nil},
		{"mariadb preparing", []replicaChannel{{IO: "Preparing", SQL: "Yes", Lag: f64(1)}}, "stopped", f64(1)},
		{"running NULL lag is unknown, never 0", []replicaChannel{{IO: "Yes", SQL: "Yes"}}, "running", nil},
		{"not running wins the wording", []replicaChannel{{IO: "Yes", SQL: "Yes"}, {IO: "No", SQL: "No"}}, "stopped", nil},
		{"only group replication channels", []replicaChannel{{Name: "group_replication_applier", IO: "No", SQL: "Yes"}, {Name: "group_replication_recovery", IO: "No", SQL: "No"}}, "none", f64(0)},
		{"group replication channels are skipped", []replicaChannel{{Name: "group_replication_applier", IO: "No", SQL: "No"}, {Name: "src", IO: "Yes", SQL: "Yes", Lag: f64(2)}}, "running", f64(2)},
		{"worst state wins", []replicaChannel{
			{IO: "Yes", SQL: "Yes", Lag: f64(1)},
			{IO: "Connecting", SQL: "Yes", Lag: f64(4)},
		}, "connecting", f64(4)},
		{"stopped beats connecting", []replicaChannel{
			{IO: "Connecting", SQL: "Yes", Lag: f64(4)},
			{IO: "No", SQL: "No", Lag: f64(2)},
			{IO: "Yes", SQL: "Yes", Lag: f64(1)},
		}, "stopped", f64(4)},
		{"largest lag across running channels", []replicaChannel{
			{IO: "Yes", SQL: "Yes", Lag: f64(7)},
			{IO: "Yes", SQL: "Yes", Lag: f64(30)},
		}, "running", f64(30)},
		{"a NULL lag on a stopped channel wins over other lags", []replicaChannel{
			{IO: "Yes", SQL: "Yes", Lag: f64(7)},
			{IO: "Yes", SQL: "No"},
		}, "stopped", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := healthy
			st.Replicas = tc.chs
			obs, err := collect(t, newTestPlugin(&fakeSession{stats: st}, nil, nil), goodFields)
			if err != nil {
				t.Fatal(err)
			}
			mustLabel(t, obs, "", "mysql.replication_state", tc.state)
			if tc.lag == nil {
				want := "mysql: replication is not running"
				if tc.name == "running NULL lag is unknown, never 0" {
					want = "mysql: replication lag is unknown"
				}
				mustErr(t, obs, "", "mysql.replication_lag_seconds", want)
			} else {
				mustValue(t, obs, "", "mysql.replication_lag_seconds", *tc.lag)
			}
		})
	}
}

func TestFailuresReportDownWithTheReason(t *testing.T) {
	tests := []struct {
		name string
		conn error
		qry  error
		want string
	}{
		{"auth at connect", myErr(1045, "Access denied for user '"+plantedUser+"'"), nil, "mysql: authentication failed (error 1045)"},
		{"permission on query", nil, myErr(1227, "Access denied; you need (at least one of) the PROCESS privilege(s)"), "mysql: permission denied (error 1227)"},
		{"permission 1142", nil, myErr(1142, "x"), "mysql: permission denied (error 1142)"},
		{"server error", nil, myErr(1040, "Too many connections"), "mysql: server error (error 1040)"},
		{"no TLS", errNoTLSForTest, nil, "mysql: TLS handshake failed"},
		{"opaque", errors.New("dial " + plantedHost + " " + plantedPassword), nil, "mysql: connection failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPlugin(&fakeSession{statsErr: tc.qry}, tc.conn, nil)
			obs, err := collect(t, p, goodFields)
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "", "mysql.up", 0)
			for _, m := range hostMetrics {
				mustErr(t, obs, "", m, tc.want)
			}
			noSecretIn(t, obs, err)
		})
	}
}

func TestSecretFieldProblems(t *testing.T) {
	p := newTestPlugin(&fakeSession{stats: healthy}, nil, nil)
	obs, err := collect(t, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("obs %+v", obs)
	}
	mustErr(t, obs, "", "mysql.up", "mysql: no secret given")

	obs, _ = collect(t, p, map[string]string{"password": plantedPassword})
	mustErr(t, obs, "", "mysql.up", "mysql: secret field `user` is required")
	obs, _ = collect(t, p, map[string]string{"user": plantedUser, "port": "x" + plantedPassword})
	mustErr(t, obs, "", "mysql.up", "mysql: secret field `port` is not a valid port")
	noSecretIn(t, obs, nil)
}

func TestCallerContextEndsWithItsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	obs, err := newTestPlugin(&fakeSession{stats: healthy}, nil, nil).CollectWithSecret(ctx, nil, goodFields)
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("obs %+v err %v", obs, err)
	}

	// Cancelled while connecting: still nothing but the error.
	ctx, cancel = context.WithCancel(context.Background())
	p := &plugin{now: time.Now, connect: func(ctx context.Context, _ connSettings) (session, error) {
		cancel()
		return nil, ctx.Err()
	}}
	obs, err = p.CollectWithSecret(ctx, nil, goodFields)
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("obs %+v err %v", obs, err)
	}
}

// The plugin's own deadline is a slow server, reported as down.
func TestOwnDeadlineIsTimedOut(t *testing.T) {
	p := &plugin{now: time.Now, timeout: 50 * time.Millisecond, connect: func(ctx context.Context, _ connSettings) (session, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	obs, err := collect(t, p, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "mysql.up", 0)
	mustErr(t, obs, "", "mysql.replication_state", "mysql: timed out")
}

func TestNoSecretInAnyFailure(t *testing.T) {
	leaky := errors.New(strings.Repeat("x", 3) + plantedUser + plantedHost + plantedPassword)
	for _, p := range []*plugin{
		newTestPlugin(&fakeSession{statsErr: leaky}, nil, nil),
		newTestPlugin(nil, leaky, nil),
	} {
		obs, err := collect(t, p, goodFields)
		noSecretIn(t, obs, err)
	}
}

// A server restart resets Slow_queries; Uptime falling is the signal, even
// when the counter has already climbed past its old value.
func TestUptimeDropResetsTheSlowQueryBaseline(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{stats: healthy}
	sess.stats.Uptime = 1000
	p := newTestPlugin(sess, nil, nil)
	p.now = c.now
	_, _ = collect(t, p, goodFields)
	// Restarted, and 500 slow queries since: higher than before the restart.
	sess.stats.Uptime, sess.stats.SlowQueries = 10, 600
	c.t = c.t.Add(10 * time.Second)
	obs, _ := collect(t, p, goodFields)
	if _, ok := find(obs, "", "mysql.slow_queries_per_s"); ok {
		t.Fatal("a rate across a restart")
	}
	sess.stats.Uptime, sess.stats.SlowQueries = 20, 630
	c.t = c.t.Add(10 * time.Second)
	obs, _ = collect(t, p, goodFields)
	mustValue(t, obs, "", "mysql.slow_queries_per_s", 3)
}
