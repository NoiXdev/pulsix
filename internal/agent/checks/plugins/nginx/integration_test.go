package nginx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const statusConf = `server {
    listen 80;
    location = /nginx_status {
        stub_status;
    }
    location / {
        return 200 "ok\n";
    }
}
`

// startNginx runs nginx:1.27-alpine with stub_status at /nginx_status and
// returns the page's URL.
func startNginx(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nginx:1.27-alpine",
			ExposedPorts: []string{"80/tcp"},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(statusConf),
				ContainerFilePath: "/etc/nginx/conf.d/default.conf",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/nginx_status").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start nginx:1.27-alpine: %v", err)
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
	return "http://" + host + ":" + port.Port() + "/nginx_status"
}

// TestAgainstARealServer runs the plugin against nginx 1.27 with stub_status.
func TestAgainstARealServer(t *testing.T) {
	url := startNginx(t)
	p, clk := newTestPlugin()
	params := map[string]any{"url": url}

	obs, err := p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	if o, ok := find(obs, "nginx.connections_active"); !ok || o.Value == nil || *o.Value < 1 {
		t.Fatalf("connections_active = %+v, want >= 1", o)
	}
	mustAbsent(t, obs, "nginx.requests_per_s")

	// Each fetch is itself a request and a connection.
	clk.Advance(10 * time.Second)
	obs, err = p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	for _, m := range []string{"nginx.requests_per_s", "nginx.connections_accepted_per_s"} {
		if o, ok := find(obs, m); !ok || o.Value == nil || *o.Value <= 0 {
			t.Fatalf("%s = %+v, want a positive rate", m, o)
		}
	}
	mustValue(t, obs, "nginx.connections_dropped_per_s", 0)
}
