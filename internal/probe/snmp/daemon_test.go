package snmp_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

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

// requireLinuxDocker skips the test unless a Docker daemon that runs Linux
// containers answers; with PULSIX_TEST_REQUIRE_DOCKER=1 it fails instead,
// so a CI job that must run it cannot pass by skipping.
func requireLinuxDocker(t *testing.T) {
	t.Helper()
	skip := t.Skipf
	if os.Getenv("PULSIX_TEST_REQUIRE_DOCKER") == "1" {
		skip = t.Fatalf
	}
	if _, err := exec.LookPath("docker"); err != nil {
		skip("no docker: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.OSType}}").Output()
	if err != nil {
		skip("docker daemon unavailable: %v", err)
		return
	}
	if os := strings.TrimSpace(string(out)); os != "linux" {
		skip("docker runs %s containers, the test needs linux", os)
	}
}

// startSNMPD runs net-snmp's daemon in a throwaway container named
// pulsix-sdd-snmpd-<random>, published on a random loopback UDP port, and
// returns that port once the daemon answers. The container is removed
// when the test ends, whatever its outcome; no other container is touched.
func startSNMPD(t *testing.T) int {
	t.Helper()
	requireLinuxDocker(t)
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "pulsix-sdd-snmpd-" + hex.EncodeToString(b[:])
	docker := func(timeout time.Duration, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() { _, _ = docker(time.Minute, "rm", "-f", name) })

	script := `apk add --no-cache net-snmp >/dev/null && printf '%s' "$SNMPD_CONF" > /etc/snmp/snmpd.conf && exec snmpd -f -Lo -C -c /etc/snmp/snmpd.conf udp:161`
	if out, err := docker(5*time.Minute, "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::161/udp",
		"-e", "SNMPD_CONF="+snmpdConf, snmpdImage, "sh", "-c", script); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	out, err := docker(time.Minute, "port", name, "161/udp")
	if err != nil {
		t.Fatalf("docker port: %v: %s", err, out)
	}
	_, portStr, err := net.SplitHostPort(strings.Fields(out)[0])
	if err != nil {
		t.Fatalf("docker port %q: %v", out, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	// The container installs net-snmp first; wait until the daemon answers.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		obs := snmp.Probe(context.Background(), probetest.LoopbackGuard(), daemonParams(port, "v2c"), "127.0.0.1", v2c)
		if len(obs) > 0 && obs[0].Value != nil && *obs[0].Value == 1 {
			return port
		}
		if running, _ := docker(time.Minute, "inspect", "-f", "{{.State.Running}}", name); running != "true" {
			logs, _ := docker(time.Minute, "logs", name)
			t.Fatalf("snmpd container stopped: %s", logs)
		}
		if time.Now().After(deadline) {
			t.Fatal("snmpd did not answer within 3 minutes")
		}
		time.Sleep(time.Second)
	}
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
			if len(obs) != 6 {
				t.Fatalf("user %s: %+v", secret["username"], obs)
			}
			for _, o := range obs {
				if o.Err != `the device did not accept the credentials of secret "lab"` {
					t.Fatalf("user %s: %+v", secret["username"], obs)
				}
			}
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
		probetest.WantOnly(t, obs, "snmp.up", 0)
	})
}
