package phpfpm

import (
	"context"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/containertest"
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
	containertest.RequireLinuxDocker(t)
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

// openPoolConf is a second pool with every script extension allowed
// (security.limit_extensions emptied, which FPM documents as "allow
// all"), the setting under which only the request itself keeps an
// arbitrary file from running as PHP.
const openPoolConf = `[open]
user = www-data
group = www-data
listen = 9001
pm = static
pm.max_children = 1
pm.status_path = /status
security.limit_extensions =
`

// docrootPoolConf is a third pool that also allows every extension, with
// cgi.fix_pathinfo off and doc_root set: FPM then opens doc_root plus the
// request URI (from SCRIPT_NAME) as the script whenever SCRIPT_FILENAME
// is a valid path at all.
const docrootPoolConf = `[docroot]
user = www-data
group = www-data
listen = 9002
pm = static
pm.max_children = 1
pm.status_path = /status
security.limit_extensions =
php_admin_value[cgi.fix_pathinfo] = 0
php_admin_value[doc_root] = /var/www/html
`

// runmeScript is a dotless file that is PHP: run, it leaves a marker and
// prints a valid status page, so either would show.
const runmeScript = `<?php file_put_contents('/tmp/executed', 'yes');
header('Content-Type: application/json');
echo '{"pool":"x","process manager":"static","start time":1,"start since":1,"accepted conn":1,"listen queue":0,` +
	`"max listen queue":0,"listen queue len":0,"idle processes":1,"active processes":1,"total processes":2,` +
	`"max active processes":1,"max children reached":0,"slow requests":0}';
`

// fpmDirect is a php:8.3-fpm-alpine container with no web server: the
// image's www pool (default limit_extensions) on def, the open pool on
// open, the docroot pool on docroot, all with their status page on
// /status.
type fpmDirect struct {
	c                  testcontainers.Container
	def, open, docroot string
}

// pools is every pool's address.
func (f fpmDirect) pools() []string { return []string{f.open, f.docroot, f.def} }

func startFPMDirect(t *testing.T) fpmDirect {
	t.Helper()
	ctx := context.Background()
	files := []testcontainers.ContainerFile{
		{Reader: strings.NewReader(poolConf), ContainerFilePath: "/usr/local/etc/php-fpm.d/zz-status.conf", FileMode: 0o644},
		{Reader: strings.NewReader(openPoolConf), ContainerFilePath: "/usr/local/etc/php-fpm.d/zz-open.conf", FileMode: 0o644},
		{Reader: strings.NewReader(docrootPoolConf), ContainerFilePath: "/usr/local/etc/php-fpm.d/zz-docroot.conf", FileMode: 0o644},
		{Reader: strings.NewReader(runmeScript), ContainerFilePath: "/var/www/html/runme", FileMode: 0o755},
	}
	fpm, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "php:8.3-fpm-alpine",
			ExposedPorts: []string{"9000/tcp", "9001/tcp", "9002/tcp"},
			Files:        files,
			WaitingFor: wait.ForAll(
				wait.ForLog("ready to handle connections"),
				wait.ForListeningPort("9000/tcp"),
				wait.ForListeningPort("9001/tcp"),
				wait.ForListeningPort("9002/tcp"),
			).WithDeadline(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start php:8.3-fpm-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(fpm) })
	host, err := fpm.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	def, err := fpm.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	open, err := fpm.MappedPort(ctx, "9001/tcp")
	if err != nil {
		t.Fatal(err)
	}
	docroot, err := fpm.MappedPort(ctx, "9002/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return fpmDirect{c: fpm, def: net.JoinHostPort(host, def.Port()), open: net.JoinHostPort(host, open.Port()),
		docroot: net.JoinHostPort(host, docroot.Port())}
}

// executed reports whether runme ran as PHP.
func (f fpmDirect) executed(t *testing.T) bool {
	t.Helper()
	code, _, err := f.c.Exec(context.Background(), []string{"test", "-e", "/tmp/executed"})
	if err != nil {
		t.Fatal(err)
	}
	return code == 0
}

// TestFastCGIAgainstARealServer reads PHP-FPM 8.3's status page over
// FastCGI with no web server in between (backlog 3.11a): from the
// default pool and from the two pools that allow every script extension.
func TestFastCGIAgainstARealServer(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	f := startFPMDirect(t)
	for _, addr := range f.pools() {
		e := "fcgi://" + addr + "/status"
		p, clk := newTestPlugin()
		params := map[string]any{"urls": []any{e}}

		obs := collect(t, p, params)
		mustItems(t, obs, "", e)
		mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
		mustValueAt(t, obs, e, "phpfpm.up", 1)
		if o, ok := findAt(obs, e, "phpfpm.processes_total"); !ok || o.Value == nil || *o.Value < 1 {
			t.Fatalf("processes_total = %+v, want >= 1", o)
		}
		clk.Advance(10 * time.Second)
		obs = collect(t, p, params)
		if o, ok := findAt(obs, e, "phpfpm.requests_per_s"); !ok || o.Value == nil || *o.Value <= 0 {
			t.Fatalf("requests_per_s = %+v, want a positive rate", o)
		}
	}
	t.Run("a path that is not the status path runs nothing", func(t *testing.T) {
		testFastCGIUnknownPathIsNotRun(t, f)
	})
}

// testFastCGIUnknownPathIsNotRun: a path that is not the pool's status
// path never runs a file, not even a dotless PHP file in a pool whose
// limit_extensions allows everything, nor one under doc_root in a pool
// with cgi.fix_pathinfo off (whose lookup uses SCRIPT_NAME) — the request
// never names a script (SCRIPT_FILENAME is always "/.."). Every such path
// reads the same 404 in these pools, so the answer tells nothing about
// which files exist.
func testFastCGIUnknownPathIsNotRun(t *testing.T, f fpmDirect) {
	for _, addr := range f.pools() {
		var reasons []string
		for _, path := range []string{"/var/www/html/runme", "/runme", "/nope", "/etc/hostname"} {
			e := "fcgi://" + addr + path
			p, _ := newTestPlugin()
			obs := collect(t, p, map[string]any{"urls": []any{e}})
			mustValueAt(t, obs, e, "phpfpm.up", 0)
			o, _ := findAt(obs, e, "phpfpm.listen_queue")
			reasons = append(reasons, o.Err)
		}
		if f.executed(t) {
			t.Fatalf("a path that is not the status path ran a script on %s", addr)
		}
		if len(slices.Compact(slices.Clone(reasons))) != 1 {
			t.Fatalf("answers differ by path on %s: %q", addr, reasons)
		}
		if reasons[0] != "phpfpm: HTTP status 404" {
			t.Fatalf("%s answers %q, want FPM's no-script 404", addr, reasons[0])
		}
	}
}
