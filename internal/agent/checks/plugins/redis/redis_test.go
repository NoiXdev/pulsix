package redis

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

func collect(t *testing.T, p *plugin, fields map[string]string) []checks.Observation {
	t.Helper()
	obs, err := p.CollectWithSecret(context.Background(), nil, fields)
	if err != nil {
		t.Fatal(err)
	}
	noSecretIn(t, obs, err)
	return obs
}

func TestPrimary(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo()))
	p, _ := newTestPlugin()
	obs := collect(t, p, fieldsFor(s, nil))
	mustValue(t, obs, "redis.up", 1)
	mustValue(t, obs, "redis.memory_used_bytes", 104857600)
	mustValue(t, obs, "redis.memory_max_bytes", 1073741824)
	mustValue(t, obs, "redis.memory_pct", 100.0*104857600/1073741824)
	mustValue(t, obs, "redis.clients", 7)
	mustLabel(t, obs, "redis.role", "primary")
	mustLabel(t, obs, "redis.replication_link", "none")
	mustValue(t, obs, "redis.replication_lag_seconds", 0)
	mustAbsent(t, obs, "redis.evicted_keys_per_s") // first round
	if len(obs) != 8 {
		t.Fatalf("%d observations: %+v", len(obs), obs)
	}
	if got := s.received(); got != cmd("INFO") {
		t.Fatalf("sent %q, want only INFO", got)
	}
}

func TestEveryMetricIsOneOfTheHostMetrics(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo()))
	p, _ := newTestPlugin()
	obs := collect(t, p, fieldsFor(s, nil))
	known := map[string]bool{metricUp: true}
	for _, m := range hostMetrics {
		known[m] = true
	}
	for _, o := range obs {
		if !known[o.Metric] || o.Item != "" {
			t.Errorf("unexpected %+v", o)
		}
	}
}

func TestReplica(t *testing.T) {
	tests := []struct {
		name    string
		extra   []string
		link    string
		lag     float64
		lagErr  string
		wantErr bool
	}{
		{"link up", []string{"role", "slave", "master_link_status", "up", "master_last_io_seconds_ago", "3"}, "up", 3, "", false},
		{"link up, just read", []string{"role", "slave", "master_link_status", "up", "master_last_io_seconds_ago", "0"}, "up", 0, "", false},
		{"link down", []string{"role", "slave", "master_link_status", "down", "master_last_io_seconds_ago", "-1"}, "down", 0, "redis: replication link is down", true},
		{"link down with a stale value", []string{"role", "slave", "master_link_status", "down", "master_last_io_seconds_ago", "40"}, "down", 0, "redis: replication link is down", true},
		{"link status missing", []string{"role", "slave"}, "down", 0, "redis: replication link is down", true},
		{"up but io -1", []string{"role", "slave", "master_link_status", "up", "master_last_io_seconds_ago", "-1"}, "up", 0, "redis: replication lag is unknown", true},
		{"up but io missing", []string{"role", "slave", "master_link_status", "up"}, "up", 0, "redis: replication lag is unknown", true},
		{"up but io junk", []string{"role", "slave", "master_link_status", "up", "master_last_io_seconds_ago", "soon"}, "up", 0, "redis: replication lag is unknown", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := serve(t, nil, bulk(primaryInfo(tc.extra...)))
			p, _ := newTestPlugin()
			obs := collect(t, p, fieldsFor(s, nil))
			mustValue(t, obs, "redis.up", 1)
			mustLabel(t, obs, "redis.role", "replica")
			mustLabel(t, obs, "redis.replication_link", tc.link)
			if tc.wantErr {
				mustErr(t, obs, "redis.replication_lag_seconds", tc.lagErr)
			} else {
				mustValue(t, obs, "redis.replication_lag_seconds", tc.lag)
			}
		})
	}
}

func TestMemoryLimit(t *testing.T) {
	t.Run("maxmemory 0 falls back to the system memory", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("maxmemory", "0")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustValue(t, obs, "redis.memory_max_bytes", 8589934592)
		mustValue(t, obs, "redis.memory_pct", 100.0*104857600/8589934592)
	})
	t.Run("maxmemory missing falls back too", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("maxmemory", "-")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustValue(t, obs, "redis.memory_max_bytes", 8589934592)
	})
	t.Run("no limit known", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("maxmemory", "0", "total_system_memory", "0")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustValue(t, obs, "redis.up", 1)
		mustValue(t, obs, "redis.memory_used_bytes", 104857600)
		mustErr(t, obs, "redis.memory_max_bytes", "redis: memory limit unknown")
		mustErr(t, obs, "redis.memory_pct", "redis: memory limit unknown")
		mustValue(t, obs, "redis.clients", 7)
	})
	t.Run("neither field present", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("maxmemory", "-", "total_system_memory", "-")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustErr(t, obs, "redis.memory_max_bytes", "redis: memory limit unknown")
		mustErr(t, obs, "redis.memory_pct", "redis: memory limit unknown")
	})
}

// One odd field must not hide the other metrics.
func TestMissingFieldsAreReportedPerMetric(t *testing.T) {
	t.Run("used_memory", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("used_memory", "-")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustValue(t, obs, "redis.up", 1)
		mustErr(t, obs, "redis.memory_used_bytes", "redis: used_memory is not reported")
		mustErr(t, obs, "redis.memory_pct", "redis: used_memory is not reported")
		mustValue(t, obs, "redis.memory_max_bytes", 1073741824)
		mustValue(t, obs, "redis.clients", 7)
		mustLabel(t, obs, "redis.role", "primary")
	})
	t.Run("connected_clients", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("connected_clients", "junk")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustErr(t, obs, "redis.clients", "redis: connected_clients is not reported")
		mustValue(t, obs, "redis.memory_used_bytes", 104857600)
	})
	t.Run("role", func(t *testing.T) {
		for _, role := range []string{"sentinel", "", "-"} {
			s := serve(t, nil, bulk(primaryInfo("role", role)))
			p, _ := newTestPlugin()
			obs := collect(t, p, fieldsFor(s, nil))
			mustValue(t, obs, "redis.up", 1)
			mustErr(t, obs, "redis.role", "redis: role is not primary or replica")
			mustErr(t, obs, "redis.replication_link", "redis: role is not primary or replica")
			mustErr(t, obs, "redis.replication_lag_seconds", "redis: role is not primary or replica")
			mustValue(t, obs, "redis.clients", 7)
		}
	})
	t.Run("evicted_keys", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo("evicted_keys", "-")))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustErr(t, obs, "redis.evicted_keys_per_s", "redis: evicted_keys is not reported")
	})
}

func TestInfoParsingToleratesNoise(t *testing.T) {
	text := "# Server\r\n\r\n# comment: not a field\r\nno separator here\r\n:novalue\r\n" +
		"used_memory:1000\r\nmaxmemory:2000\r\nconnected_clients:2\r\nrole:master\r\nevicted_keys:0\r\n" +
		"used_memory:2000\r\n" // the last duplicate wins
	s := serve(t, nil, bulk(text))
	p, clk := newTestPlugin()
	obs := collect(t, p, fieldsFor(s, nil))
	mustValue(t, obs, "redis.memory_used_bytes", 2000)
	mustValue(t, obs, "redis.memory_pct", 100)
	// uptime_in_seconds absent: the first round has no rate yet, and a
	// second one still yields it.
	mustAbsent(t, obs, "redis.evicted_keys_per_s")
	clk.t = clk.t.Add(10 * time.Second)
	text2 := strings.Replace(text, "evicted_keys:0", "evicted_keys:30", 1)
	s = serve(t, nil, bulk(text2))
	mustValue(t, collect(t, p, fieldsFor(s, nil)), "redis.evicted_keys_per_s", 3)
}

func TestEvictionRate(t *testing.T) {
	p, clk := newTestPlugin()
	round := func(extra ...string) []checks.Observation {
		s := serve(t, nil, bulk(primaryInfo(extra...)))
		return collect(t, p, fieldsFor(s, nil))
	}
	mustAbsent(t, round("evicted_keys", "100", "uptime_in_seconds", "1000"), "redis.evicted_keys_per_s")
	clk.t = clk.t.Add(10 * time.Second)
	mustValue(t, round("evicted_keys", "150", "uptime_in_seconds", "1010"), "redis.evicted_keys_per_s", 5)
	clk.t = clk.t.Add(10 * time.Second)
	mustValue(t, round("evicted_keys", "150", "uptime_in_seconds", "1020"), "redis.evicted_keys_per_s", 0)

	// A restart: the counter began again, here past its old value. The
	// uptime drop resets the baseline, so no rate this round (not 850/10).
	clk.t = clk.t.Add(10 * time.Second)
	mustAbsent(t, round("evicted_keys", "1000", "uptime_in_seconds", "5"), "redis.evicted_keys_per_s")
	clk.t = clk.t.Add(10 * time.Second)
	mustValue(t, round("evicted_keys", "1020", "uptime_in_seconds", "15"), "redis.evicted_keys_per_s", 2)

	// A counter that fell without an uptime drop (CONFIG RESETSTAT) is a
	// reset too.
	clk.t = clk.t.Add(10 * time.Second)
	mustAbsent(t, round("evicted_keys", "3", "uptime_in_seconds", "25"), "redis.evicted_keys_per_s")
}

// A failed round does not disturb the baseline.
func TestEvictionBaselineSurvivesAFailedRound(t *testing.T) {
	p, clk := newTestPlugin()
	s := serve(t, nil, bulk(primaryInfo("evicted_keys", "100")))
	collect(t, p, fieldsFor(s, nil))
	clk.t = clk.t.Add(10 * time.Second)
	collect(t, p, map[string]string{"port": "1", "host": "127.0.0.1"}) // refused
	clk.t = clk.t.Add(10 * time.Second)
	s = serve(t, nil, bulk(primaryInfo("evicted_keys", "140")))
	mustValue(t, collect(t, p, fieldsFor(s, nil)), "redis.evicted_keys_per_s", 2)
}

func TestInstancesKeepTheirOwnBaseline(t *testing.T) {
	proto, clk := newTestPlugin()
	a := proto.NewInstance().(*plugin)
	b := proto.NewInstance().(*plugin)
	run := func(p *plugin, evicted int) []checks.Observation {
		s := serve(t, nil, bulk(primaryInfo("evicted_keys", strconv.Itoa(evicted))))
		return collect(t, p, fieldsFor(s, nil))
	}
	run(a, 100)
	run(b, 5000)
	clk.t = clk.t.Add(10 * time.Second)
	mustValue(t, run(a, 200), "redis.evicted_keys_per_s", 10)
	mustValue(t, run(b, 5010), "redis.evicted_keys_per_s", 1)
}

func TestAuth(t *testing.T) {
	t.Run("password only", func(t *testing.T) {
		s := serve(t, nil, "+OK\r\n", bulk(primaryInfo()))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, map[string]string{"password": plantedPassword}))
		mustValue(t, obs, "redis.up", 1)
		if got, want := s.received(), cmd("AUTH", plantedPassword)+cmd("INFO"); got != want {
			t.Fatalf("sent %q, want %q", got, want)
		}
	})
	t.Run("user and password", func(t *testing.T) {
		s := serve(t, nil, "+OK\r\n", bulk(primaryInfo()))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, map[string]string{"user": plantedUser, "password": plantedPassword}))
		mustValue(t, obs, "redis.up", 1)
		if got, want := s.received(), cmd("AUTH", plantedUser, plantedPassword)+cmd("INFO"); got != want {
			t.Fatalf("sent %q, want %q", got, want)
		}
	})
	t.Run("binary-unsafe characters are sent as a bulk string", func(t *testing.T) {
		pw := "a b\r\n*1\r\n$4\r\nQUIT\r\n\"'"
		s := serve(t, nil, "+OK\r\n", bulk(primaryInfo()))
		p, _ := newTestPlugin()
		collect(t, p, fieldsFor(s, map[string]string{"password": pw}))
		if got, want := s.received(), cmd("AUTH", pw)+cmd("INFO"); got != want {
			t.Fatalf("sent %q, want %q", got, want)
		}
	})
	t.Run("no password sends no AUTH", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo()))
		p, _ := newTestPlugin()
		collect(t, p, fieldsFor(s, nil))
		if got := s.received(); got != cmd("INFO") {
			t.Fatalf("sent %q", got)
		}
	})
	t.Run("AUTH answered with something other than OK", func(t *testing.T) {
		s := serve(t, nil, ":1\r\n")
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, map[string]string{"password": plantedPassword}))
		mustDown(t, obs, "unexpected server response")
	})
}

func TestNoSecretReadsTheDefaultAddressWithoutAuth(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo()))
	p, _ := newTestPlugin()
	p.defaultAddr = s.addr()
	obs, err := p.CollectWithSecret(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "redis.up", 1)
	if got := s.received(); got != cmd("INFO") {
		t.Fatalf("sent %q", got)
	}
	// Collect, the secret-less entry point, is the same.
	s = serve(t, nil, bulk(primaryInfo()))
	p.defaultAddr = s.addr()
	obs, err = p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "redis.up", 1)
}

func TestServerErrors(t *testing.T) {
	tests := []struct {
		name    string
		replies []string
		fields  map[string]string
		reason  string
	}{
		{"NOAUTH on INFO", []string{"-NOAUTH Authentication required.\r\n"}, nil, "authentication failed"},
		{"WRONGPASS on AUTH", []string{"-WRONGPASS invalid username-password pair or user is disabled.\r\n"}, map[string]string{"user": plantedUser, "password": plantedPassword}, "authentication failed"},
		{"invalid password", []string{"-ERR invalid password\r\n"}, map[string]string{"password": plantedPassword}, "authentication failed"},
		{"no password is set", []string{"-ERR Client sent AUTH, but no password is set\r\n"}, map[string]string{"password": plantedPassword}, "authentication failed"},
		{"NOPERM on INFO", []string{"+OK\r\n", "-NOPERM this user has no permissions to run the 'info' command " + plantedUser + "\r\n"}, map[string]string{"password": plantedPassword}, "permission denied"},
		{"other error", []string{"-ERR something " + plantedPassword + " " + plantedHost + "\r\n"}, nil, "server error"},
		{"not RESP", []string{"HTTP/1.1 400 Bad Request " + plantedHost + "\r\n"}, nil, "unexpected server response"},
		{"INFO is not a bulk string", []string{"+OK\r\n"}, nil, "unexpected server response"},
		{"INFO is null", []string{"$-1\r\n"}, nil, "unexpected server response"},
		{"oversized length", []string{"$999999999999\r\n"}, nil, "unexpected server response"},
		{"over 1 MiB", []string{"$1048577\r\n"}, nil, "unexpected server response"},
		{"array", []string{"*1\r\n$1\r\na\r\n"}, nil, "unexpected server response"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := serve(t, nil, tc.replies...)
			p, _ := newTestPlugin()
			mustDown(t, collect(t, p, fieldsFor(s, tc.fields)), tc.reason)
		})
	}
}

func TestInvalidSecretFieldsAreAConfigurationError(t *testing.T) {
	p, _ := newTestPlugin()
	obs := collect(t, p, map[string]string{"port": plantedHost, "password": plantedPassword})
	if len(obs) != 1 {
		t.Fatalf("obs = %+v", obs)
	}
	mustErr(t, obs, "redis.up", "redis: secret field `port` is not a valid port")
}

func TestCallerContext(t *testing.T) {
	p, _ := newTestPlugin()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	obs, err := p.CollectWithSecret(ctx, nil, map[string]string{"port": "1"})
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("obs = %+v, err = %v", obs, err)
	}
}

func TestPluginShape(t *testing.T) {
	p := New().(*plugin)
	if p.Name() != "redis" || p.SecretErrorMetric() != "redis.up" {
		t.Fatalf("name %q, secret metric %q", p.Name(), p.SecretErrorMetric())
	}
	inst, ok := checks.Plugin(p).(checks.StatefulPlugin)
	if !ok {
		t.Fatal("not a StatefulPlugin")
	}
	if inst.NewInstance() == checks.Plugin(p) {
		t.Fatal("NewInstance returned the prototype")
	}
}
