package redis

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/containertest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	serverPassword = "requirepass-pw"
	monitorUser    = "monitor"
	monitorPass    = "monitor-pw"
)

// startServer runs redis:7.4-alpine with a password, a small maxmemory with
// an evicting policy, and an ACL user that may run INFO and AUTH only. It
// returns the host and mapped port.
func startServer(t *testing.T) (host, port string) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7.4-alpine",
			ExposedPorts: []string{"6379/tcp"},
			Cmd: []string{
				"redis-server", "--requirepass", serverPassword,
				"--maxmemory", "2mb", "--maxmemory-policy", "allkeys-lru",
				"--user", monitorUser, "on", ">" + monitorPass, "+info", "+auth",
			},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start redis:7.4-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	if host, err = c.Host(ctx); err != nil {
		t.Fatal(err)
	}
	mapped, err := c.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	port = mapped.Port()

	// The port can listen before the server answers.
	deadline := time.Now().Add(30 * time.Second)
	for {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cl, err := dial(cctx, settings{network: "tcp", addr: host + ":" + port, host: host, tls: "false"})
		if err == nil {
			_, err = cl.do("PING")
			cl.Close()
		}
		cancel()
		if err == nil || strings.Contains(err.Error(), "authentication failed") {
			return host, port
		}
		if time.Now().After(deadline) {
			t.Fatalf("redis did not answer: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestAgainstARealServer runs the plugin against Redis 7.4 started with
// requirepass.
func TestAgainstARealServer(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	host, port := startServer(t)
	ctx := context.Background()
	fields := func(extra map[string]string) map[string]string {
		f := map[string]string{"host": host, "port": port}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	allMetrics := append([]string{"redis.up"}, hostMetrics...)

	t.Run("healthy primary with the password", func(t *testing.T) {
		obs, err := New().(*plugin).CollectWithSecret(ctx, nil, fields(map[string]string{"password": serverPassword}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "redis.up", 1)
		for _, m := range allMetrics {
			if m == "redis.evicted_keys_per_s" {
				mustAbsent(t, obs, m) // first round
				continue
			}
			if _, ok := find(obs, "", m); !ok {
				t.Errorf("no %s in %+v", m, obs)
			}
		}
		used, _ := find(obs, "", "redis.memory_used_bytes")
		if used.Value == nil || *used.Value <= 0 {
			t.Errorf("used = %+v", used)
		}
		mustValue(t, obs, "redis.memory_max_bytes", 2<<20)
		pct, _ := find(obs, "", "redis.memory_pct")
		if pct.Value == nil || *pct.Value <= 0 {
			t.Errorf("pct = %+v", pct)
		}
		clients, _ := find(obs, "", "redis.clients")
		if clients.Value == nil || *clients.Value < 1 {
			t.Errorf("clients = %+v", clients)
		}
		mustLabel(t, obs, "redis.role", "primary")
		mustLabel(t, obs, "redis.replication_link", "none")
		mustValue(t, obs, "redis.replication_lag_seconds", 0)
		// A fresh server: no snapshot attempted yet, AOF off.
		mustLabel(t, obs, "redis.rdb_last_bgsave_status", "ok")
		mustValue(t, obs, "redis.aof_enabled", 0)
		mustLabel(t, obs, "redis.aof_last_write_status", "ok")
		mustValue(t, obs, "redis.loading", 0)
		if age, _ := find(obs, "", "redis.rdb_last_save_age_seconds"); age.Value == nil || *age.Value < 0 || *age.Value > 600 {
			t.Errorf("save age = %+v", age)
		}
	})

	t.Run("an ACL user", func(t *testing.T) {
		obs, err := New().(*plugin).CollectWithSecret(ctx, nil, fields(map[string]string{"user": monitorUser, "password": monitorPass}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "redis.up", 1)
		mustLabel(t, obs, "redis.role", "primary")
	})

	t.Run("authentication failures", func(t *testing.T) {
		for name, f := range map[string]map[string]string{
			"no password":        fields(nil),
			"wrong password":     fields(map[string]string{"password": "wrong-" + plantedPassword}),
			"wrong ACL password": fields(map[string]string{"user": monitorUser, "password": "wrong-" + plantedPassword}),
			"unknown ACL user":   fields(map[string]string{"user": plantedUser, "password": plantedPassword}),
		} {
			t.Run(name, func(t *testing.T) {
				obs, err := New().(*plugin).CollectWithSecret(ctx, nil, f)
				if err != nil {
					t.Fatal(err)
				}
				noSecretIn(t, obs, err)
				mustDown(t, obs, "authentication failed")
			})
		}
	})

	t.Run("a command the ACL user lacks is permission denied", func(t *testing.T) {
		// The ACL user may only INFO and AUTH: FLUSHALL through a direct
		// client confirms the mapping against a real -NOPERM.
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cl, err := dial(cctx, settings{network: "tcp", addr: host + ":" + port, host: host, tls: "false"})
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		if err := cl.auth(monitorUser, monitorPass); err != nil {
			t.Fatal(err)
		}
		_, err = cl.do("FLUSHALL")
		if got := classify(err); got != "permission denied" {
			t.Fatalf("classify(%v) = %q", err, got)
		}
	})

	t.Run("eviction rate across two rounds of one instance", func(t *testing.T) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		inst := New().(*plugin).NewInstance().(*plugin)
		f := fields(map[string]string{"password": serverPassword})
		first, err := inst.CollectWithSecret(cctx, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		mustAbsent(t, first, "redis.evicted_keys_per_s")

		cl, err := dial(cctx, settings{network: "tcp", addr: host + ":" + port, host: host, tls: "false"})
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		if err := cl.auth("", serverPassword); err != nil {
			t.Fatal(err)
		}
		val := strings.Repeat("x", 100<<10)
		for i := range 60 { // 6 MB into a 2 MB maxmemory
			if _, err := cl.do("SET", "key"+string(rune('a'+i%26))+string(rune('a'+i/26)), val); err != nil {
				t.Fatalf("SET: %v", err)
			}
		}
		time.Sleep(1100 * time.Millisecond)
		second, err := inst.CollectWithSecret(cctx, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		o, ok := find(second, "", "redis.evicted_keys_per_s")
		if !ok || o.Value == nil || *o.Value <= 0 {
			t.Fatalf("evicted_keys_per_s = %+v in %+v", o, second)
		}
	})
}
