package caddy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/containertest"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// caddyfile turns the HTTP metrics on for every server, serves a site on :80
// and a metrics handler on :9180, so the test does not depend on the admin
// endpoint's origin checks.
const caddyfile = `{
	servers {
		metrics
	}
}
:80 {
	respond "ok"
}
:9180 {
	metrics /metrics
}
`

// startCaddy runs caddy:2.8-alpine and returns the metrics URL and the
// site's URL, both through the container's mapped ports.
func startCaddy(t *testing.T) (metricsURL, siteURL string) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "caddy:2.8-alpine",
			ExposedPorts: []string{"80/tcp", "9180/tcp"},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(caddyfile),
				ContainerFilePath: "/etc/caddy/Caddyfile",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/metrics").WithPort("9180/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start caddy:2.8-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := c.MappedPort(ctx, "9180/tcp")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := c.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + host + ":" + mp.Port() + "/metrics", "http://" + host + ":" + sp.Port() + "/"
}

// TestAgainstARealServer runs the plugin against Caddy 2.8 with the metrics
// on, after a request to the site.
func TestAgainstARealServer(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	metricsURL, siteURL := startCaddy(t)
	for range 2 {
		resp, err := http.Get(siteURL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	p, clk := newTestPlugin()
	params := map[string]any{"url": metricsURL}

	obs, err := p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.up", 1)
	if o, ok := webchecktest.Find(obs, "caddy.requests_in_flight"); !ok || o.Value == nil || *o.Value < 0 {
		t.Fatalf("requests_in_flight = %+v, want a value >= 0", o)
	}
	mustAbsent(t, obs, "caddy.requests_per_s")

	resp, err := http.Get(siteURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	clk.Advance(10 * time.Second)
	obs, err = p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.up", 1)
	if o, ok := webchecktest.Find(obs, "caddy.requests_per_s"); !ok || o.Value == nil || *o.Value <= 0 {
		t.Fatalf("requests_per_s = %+v, want a positive rate", o)
	}
	if o, ok := webchecktest.Find(obs, "caddy.errors_5xx_per_s"); !ok || o.Value == nil || *o.Value != 0 {
		t.Fatalf("errors_5xx_per_s = %+v, want 0", o)
	}
}
