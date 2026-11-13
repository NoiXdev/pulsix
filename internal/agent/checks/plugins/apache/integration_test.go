package apache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// statusConf is a minimal httpd.conf: the event MPM, mod_status at
// /server-status with ExtendedStatus On, open to everyone.
const statusConf = `ServerRoot "/usr/local/apache2"
Listen 80
LoadModule mpm_event_module modules/mod_mpm_event.so
LoadModule authz_core_module modules/mod_authz_core.so
LoadModule status_module modules/mod_status.so
LoadModule unixd_module modules/mod_unixd.so
User daemon
Group daemon
ServerName localhost
ErrorLog /proc/self/fd/2
ExtendedStatus On
<Location "/server-status">
    SetHandler server-status
    Require all granted
</Location>
`

// startApache runs httpd:2.4-alpine with mod_status and returns the page's
// URL as an operator would write it, without the query key.
func startApache(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "httpd:2.4-alpine",
			ExposedPorts: []string{"80/tcp"},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(statusConf),
				ContainerFilePath: "/usr/local/apache2/conf/httpd.conf",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/server-status?auto").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start httpd:2.4-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + host + ":" + port.Port() + "/server-status"
}

// TestAgainstARealServer runs the plugin against Apache httpd 2.4 with
// mod_status and ExtendedStatus On.
func TestAgainstARealServer(t *testing.T) {
	url := startApache(t)
	p, clk := newTestPlugin()
	params := map[string]any{"url": url}

	obs, err := p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "apache.up", 1)
	// The status request itself is a busy worker.
	if o, ok := find(obs, "apache.workers_busy"); !ok || o.Value == nil || *o.Value < 1 {
		t.Fatalf("workers_busy = %+v, want >= 1", o)
	}
	if o, ok := find(obs, "apache.workers_busy_pct"); !ok || o.Value == nil || *o.Value <= 0 || *o.Value > 100 {
		t.Fatalf("workers_busy_pct = %+v, want in (0, 100]", o)
	}
	mustAbsent(t, obs, "apache.requests_per_s")

	clk.Advance(10 * time.Second)
	obs, err = p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "apache.up", 1)
	if o, ok := find(obs, "apache.requests_per_s"); !ok || o.Value == nil || *o.Value <= 0 {
		t.Fatalf("requests_per_s = %+v, want a positive rate", o)
	}
	if o, ok := find(obs, "apache.bytes_per_s"); !ok || o.Err != "" {
		t.Fatalf("bytes_per_s = %+v, want a value", o)
	}
}
