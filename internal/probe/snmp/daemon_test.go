package snmp_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"github.com/noixdev/pulsix/internal/containertest"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
	"github.com/noixdev/pulsix/internal/probe/snmp"
)

// snmpdImage is the throwaway daemon's image, pinned by tag and digest; the
// test installs net-snmp from that release's package repository.
const snmpdImage = "alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf"

// snmpdConf configures the daemon: its sysName, a v2c community and three
// v3 users,
// authPriv with SHA-512/AES-256 and SHA-256/AES, authNoPriv with SHA. The
// values are test-only.
const snmpdConf = `rocommunity pulsix-v2c-test default
sysName pulsix-lab
createUser pulsixpriv SHA-512 "pulsix-auth-pass" AES-256 "pulsix-priv-pass"
createUser pulsixaes SHA-256 "pulsix-auth-pass" AES "pulsix-priv-pass"
createUser pulsixauth SHA "pulsix-auth-pass"
rouser pulsixpriv priv
rouser pulsixaes priv
rouser pulsixauth auth
`

// startSNMPD runs net-snmp's daemon in a throwaway container named
// pulsix-sdd-snmpd-<random> with its UDP port published on a random host
// port, and returns that port once the daemon answers. The container is
// removed when the test ends, whatever its outcome; no other container is
// touched. The container installs net-snmp from Alpine's package
// repository when it starts, so the test needs that repository reachable;
// containertest gates it on a Docker daemon running Linux containers
// (skip without one, fail under PULSIX_TEST_REQUIRE_DOCKER=1).
func startSNMPD(t *testing.T) int {
	t.Helper()
	containertest.RequireLinuxDocker(t)
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	script := `apk add --no-cache net-snmp >/dev/null && printf '%s' "$SNMPD_CONF" > /etc/snmp/snmpd.conf && exec snmpd -f -Lo -C -c /etc/snmp/snmpd.conf udp:161`
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Name:         "pulsix-sdd-snmpd-" + hex.EncodeToString(b[:]),
			Image:        snmpdImage,
			ExposedPorts: []string{"161/udp"},
			Env:          map[string]string{"SNMPD_CONF": snmpdConf},
			Cmd:          []string{"sh", "-c", script},
		},
		Started: true,
	})
	// A container that started and then failed still has to go.
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	if err != nil {
		t.Fatalf("start %s: %v", snmpdImage, err)
	}
	mapped, err := c.MappedPort(ctx, "161/udp")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	port, err := strconv.Atoi(mapped.Port())
	if err != nil {
		t.Fatal(err)
	}

	// The container installs net-snmp first; wait until the daemon answers.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		obs := snmp.Probe(ctx, probetest.LoopbackGuard(), daemonParams(port, "v2c"), "127.0.0.1", v2c)
		if len(obs) > 0 && obs[0].Value != nil && *obs[0].Value == 1 {
			return port
		}
		if state, err := c.State(ctx); err != nil || !state.Running {
			t.Fatalf("snmpd container stopped (%v): %s", err, containerLogs(ctx, c))
		}
		if time.Now().After(deadline) {
			t.Fatal("snmpd did not answer within 3 minutes")
		}
		time.Sleep(time.Second)
	}
}

// containerLogs returns c's output, for a failure message.
func containerLogs(ctx context.Context, c testcontainers.Container) string {
	r, err := c.Logs(ctx)
	if err != nil {
		return "(no logs: " + err.Error() + ")"
	}
	defer func() { _ = r.Close() }()
	out, _ := io.ReadAll(r)
	return string(out)
}

var v2c = map[string]string{"community": "pulsix-v2c-test"}

func daemonParams(port int, version string) map[string]any {
	return map[string]any{
		"secret": "lab", "version": version, "port": float64(port), "timeout_seconds": float64(1),
		"oids": []any{"sysname=1.3.6.1.2.1.1.5.0", "ifnumber=1.3.6.1.2.1.2.1.0", "nothing=1.3.6.1.2.1.1.99.0"},
	}
}

// TestRealDaemon polls net-snmp's daemon over v2c and each v3 security
// level the probe offers.
func TestRealDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a container")
	}
	port := startSNMPD(t)
	g := probetest.LoopbackGuard()
	ctx := context.Background()

	wantHost := func(t *testing.T, obs []probe.Observation) map[string]probe.Observation {
		t.Helper()
		m := map[string]probe.Observation{}
		for _, o := range obs {
			m[o.Metric+"/"+o.Item] = o
		}
		if o := m["snmp.up/"]; o.Value == nil || *o.Value != 1 {
			t.Fatalf("snmp.up: %+v (all %+v)", o, obs)
		}
		if o := m["snmp.failure/"]; o.Label == nil || *o.Label != probe.FailureNone {
			t.Fatalf("snmp.failure: %+v", o)
		}
		// The container started moments ago.
		if o := m["snmp.uptime_seconds/"]; o.Value == nil || *o.Value <= 0 || *o.Value > 3600 {
			t.Fatalf("snmp.uptime_seconds: %+v", o)
		}
		if o := m["snmp.response_ms/"]; o.Value == nil || *o.Value <= 0 || *o.Value > 1000 {
			t.Fatalf("snmp.response_ms: %+v", o)
		}
		if o := m["snmp.text/sysname"]; o.Label == nil || *o.Label != "pulsix-lab" {
			t.Fatalf("snmp.text[sysname]: %+v", o)
		}
		if o := m["snmp.value/ifnumber"]; o.Value == nil || *o.Value < 2 {
			t.Fatalf("snmp.value[ifnumber]: %+v", o)
		}
		if o := m["snmp.value/nothing"]; o.Err != "the device has no such object" {
			t.Fatalf("snmp.value[nothing]: %+v", o)
		}
		return m
	}

	t.Run("v2c with interfaces and rates", func(t *testing.T) {
		p := daemonParams(port, "v2c")
		p["interfaces"] = true
		poller := snmp.NewPoller(nil)
		obs, info := poller.Poll(ctx, g, p, "127.0.0.1", v2c)
		m := wantHost(t, obs)
		if info.InterfacesSkipped {
			t.Fatal("a container's few interfaces reported skipped")
		}
		for _, item := range []string{"lo", "eth0"} {
			if o := m["snmp.if.status/"+item]; o.Label == nil || *o.Label != "up" {
				t.Fatalf("snmp.if.status[%s]: %+v", item, o)
			}
			if _, ok := m["snmp.if.in_bps/"+item]; ok {
				t.Fatalf("first poll reported a rate for %s", item)
			}
		}
		time.Sleep(2 * time.Second)
		obs, _ = poller.Poll(ctx, g, p, "127.0.0.1", v2c)
		m = wantHost(t, obs)
		for _, metric := range []string{"snmp.if.in_bps", "snmp.if.out_bps", "snmp.if.errors"} {
			if o := m[metric+"/eth0"]; o.Value == nil || *o.Value < 0 {
				t.Fatalf("%s[eth0] on the second poll: %+v", metric, o)
			}
		}
	})

	for _, tc := range []struct {
		name   string
		secret map[string]string
	}{
		{"v3 authPriv SHA-512 AES-256", map[string]string{"username": "pulsixpriv", "auth_protocol": "SHA-512", "auth_password": "pulsix-auth-pass", "priv_protocol": "AES-256", "priv_password": "pulsix-priv-pass"}},
		{"v3 authPriv SHA-256 AES", map[string]string{"username": "pulsixaes", "auth_protocol": "SHA-256", "auth_password": "pulsix-auth-pass", "priv_protocol": "AES", "priv_password": "pulsix-priv-pass"}},
		{"v3 authNoPriv SHA", map[string]string{"username": "pulsixauth", "auth_protocol": "SHA", "auth_password": "pulsix-auth-pass"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantHost(t, snmp.Probe(ctx, g, daemonParams(port, "v3"), "127.0.0.1", tc.secret))
		})
	}

	t.Run("v3 refused credentials", func(t *testing.T) {
		for _, secret := range []map[string]string{
			{"username": "pulsixauth", "auth_protocol": "SHA", "auth_password": "wrong-password"},
			{"username": "nosuchuser", "auth_protocol": "SHA", "auth_password": "pulsix-auth-pass"},
			{"username": "pulsixpriv", "auth_protocol": "SHA-512", "auth_password": "pulsix-auth-pass", "priv_protocol": "AES-256", "priv_password": "wrong-password"},
		} {
			obs := snmp.Probe(ctx, g, daemonParams(port, "v3"), "127.0.0.1", secret)
			probetest.WantDown(t, obs, "snmp", probe.FailureAuth)
		}
	})

	// A user asking below the security level its access needs is
	// authenticated, so the device answers: up, with its refusal on every
	// value.
	t.Run("v3 below the user's level", func(t *testing.T) {
		secret := map[string]string{"username": "pulsixpriv", "auth_protocol": "SHA-512", "auth_password": "pulsix-auth-pass"}
		obs := snmp.Probe(ctx, g, daemonParams(port, "v3"), "127.0.0.1", secret)
		m := map[string]probe.Observation{}
		for _, o := range obs {
			m[o.Metric+"/"+o.Item] = o
		}
		if o := m["snmp.up/"]; o.Value == nil || *o.Value != 1 {
			t.Fatalf("snmp.up: %+v", obs)
		}
		for _, k := range []string{"snmp.uptime_seconds/", "snmp.value/sysname"} {
			if o := m[k]; o.Err != "the device answered with error AuthorizationError" {
				t.Fatalf("%s: %+v", k, o)
			}
		}
	})

	t.Run("v2c wrong community", func(t *testing.T) {
		obs := snmp.Probe(ctx, g, daemonParams(port, "v2c"), "127.0.0.1", map[string]string{"community": "wrong"})
		probetest.WantDown(t, obs, "snmp", probe.FailureTimeout)
	})
}
