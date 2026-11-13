package phpfpm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// poolConf switches the image's www pool's status page on (the image's
// zz-docker.conf already listens on 9000).
const poolConf = `[www]
pm.status_path = /status
`

// frontConf passes /status to the FPM container over FastCGI, as an
// operator's web server would.
const frontConf = `server {
    listen 80;
    location = /status {
        include fastcgi_params;
        fastcgi_param SCRIPT_NAME /status;
        fastcgi_param SCRIPT_FILENAME /status;
        fastcgi_pass fpm:9000;
    }
}
`

// startFPM runs php:8.3-fpm-alpine with its status page on and
// nginx:1.27-alpine in front of it on a network of their own, and returns
// the page's URL through nginx as an operator would write it, without the
// query key. Only nginx's port is mapped.
func startFPM(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	nw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	t.Cleanup(func() { _ = nw.Remove(context.Background()) })

	fpm, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          "php:8.3-fpm-alpine",
			Networks:       []string{nw.Name},
			NetworkAliases: map[string][]string{nw.Name: {"fpm"}},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(poolConf),
				ContainerFilePath: "/usr/local/etc/php-fpm.d/zz-status.conf",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForLog("ready to handle connections").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start php:8.3-fpm-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(fpm) })

	front, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          "nginx:1.27-alpine",
			ExposedPorts:   []string{"80/tcp"},
			Networks:       []string{nw.Name},
			NetworkAliases: map[string][]string{nw.Name: {"front"}},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(frontConf),
				ContainerFilePath: "/etc/nginx/conf.d/default.conf",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/status?json").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start nginx:1.27-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(front) })
	host, err := front.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := front.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + host + ":" + port.Port() + "/status"
}

// TestAgainstARealServer runs the plugin against PHP-FPM 8.3's status page
// behind nginx.
func TestAgainstARealServer(t *testing.T) {
	url := startFPM(t)
	p, clk := newTestPlugin()
	params := map[string]any{"urls": []any{url}}

	obs := collect(t, p, params)
	mustItems(t, obs, "", url)
	mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
	mustValueAt(t, obs, url, "phpfpm.up", 1)
	// The status request itself is an active process.
	if o, ok := findAt(obs, url, "phpfpm.processes_total"); !ok || o.Value == nil || *o.Value < 1 {
		t.Fatalf("processes_total = %+v, want >= 1", o)
	}
	if o, ok := findAt(obs, url, "phpfpm.processes_busy_pct"); !ok || o.Value == nil || *o.Value <= 0 || *o.Value > 100 {
		t.Fatalf("processes_busy_pct = %+v, want in (0, 100]", o)
	}
	mustAbsentAt(t, obs, url, "phpfpm.requests_per_s")

	clk.Advance(10 * time.Second)
	obs = collect(t, p, params)
	mustValueAt(t, obs, url, "phpfpm.up", 1)
	if o, ok := findAt(obs, url, "phpfpm.requests_per_s"); !ok || o.Value == nil || *o.Value <= 0 {
		t.Fatalf("requests_per_s = %+v, want a positive rate", o)
	}
	mustValueAt(t, obs, url, "phpfpm.max_children_reached_per_s", 0)
	mustValueAt(t, obs, url, "phpfpm.slow_requests_per_s", 0)
}
