package phpfpm

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/fastcgi/fastcgitest"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

// fcgiEntry is a FastCGI pool entry for s as an operator writes it.
func fcgiEntry(s *fastcgitest.Server) string {
	if s.Network == "unix" {
		return "unix:" + s.Address + ":/status"
	}
	return "fcgi://" + s.Address + "/status"
}

// mustNoAddress asserts no observation's text carries the server's
// address (an item is the entry as written and may).
func mustNoAddress(t *testing.T, obs []checks.Observation, addr string) {
	t.Helper()
	for _, o := range obs {
		if strings.Contains(o.Err, addr) || strings.Contains(o.Metric, addr) || (o.Label != nil && strings.Contains(*o.Label, addr)) {
			t.Fatalf("%q leaked into %+v", addr, o)
		}
	}
}

func TestFastCGIPoolOverTCPAndSocket(t *testing.T) {
	for _, s := range []*fastcgitest.Server{
		fastcgitest.NewTCP(t, fastcgitest.Respond(200, page(100, 1000, 5, 10, 3, 1))),
		fastcgitest.NewUnix(t, fastcgitest.Respond(0, page(100, 1000, 5, 10, 3, 1))),
	} {
		t.Run(s.Network, func(t *testing.T) {
			p, clk := newTestPlugin()
			e := fcgiEntry(s)
			obs := collect(t, p, urls(e))
			mustItems(t, obs, "", e)
			mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
			mustValueAt(t, obs, e, "phpfpm.up", 1)
			mustValueAt(t, obs, e, "phpfpm.processes_total", 4)
			mustValueAt(t, obs, e, "phpfpm.processes_busy_pct", 25)
			mustAbsentAt(t, obs, e, "phpfpm.requests_per_s")

			s.SetHandler(fastcgitest.Respond(200, page(100, 1100, 5, 10, 3, 1)))
			clk.Advance(10 * time.Second)
			obs = collect(t, p, urls(e))
			mustValueAt(t, obs, e, "phpfpm.requests_per_s", 10)

			r := s.Requests()[0]
			if r.Params["SCRIPT_FILENAME"] != "/.." || r.Params["SCRIPT_NAME"] != "/status" || r.Params["QUERY_STRING"] != "json" {
				t.Fatalf("params = %v", r.Params)
			}
		})
	}
}

// TestMixedHTTPAndFastCGIPools: one list, each entry read its own way;
// the HTTP entry still gets ?json, the FastCGI one is never touched.
func TestMixedHTTPAndFastCGIPools(t *testing.T) {
	srv := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	fs := fastcgitest.NewTCP(t, fastcgitest.Respond(200, page(1, 1, 0, 0, 2, 2)))
	p, _ := newTestPlugin()
	h, f := entry(srv), fcgiEntry(fs)
	obs := collect(t, p, urls(h, f))
	mustItems(t, obs, "", h, f)
	mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
	mustValueAt(t, obs, h, "phpfpm.processes_total", 2)
	mustValueAt(t, obs, f, "phpfpm.processes_total", 4)
	if uris := srv.URIs(); len(uris) != 1 || uris[0] != "/status?json" {
		t.Fatalf("HTTP request URIs = %q", uris)
	}
	if len(fs.Requests()) != 1 {
		t.Fatalf("%d FastCGI requests", len(fs.Requests()))
	}
}

func TestFastCGIFailuresReportThePoolDown(t *testing.T) {
	ok := fastcgitest.Stdout(1, fastcgitest.CGI(200, page(1, 1, 0, 0, 1, 1)), 100)
	tests := []struct {
		name string
		h    fastcgitest.Handler
		want string
	}{
		{"status page not found", fastcgitest.Respond(404, "File not found.\n"), "HTTP status 404"},
		{"refused script", fastcgitest.Respond(403, "Access denied.\n"), "permission denied (HTTP 403)"},
		{"server error", fastcgitest.Respond(500, ""), "HTTP status 500"},
		{"not the status page", fastcgitest.Respond(200, "<html>"), errFormatTx},
		{"broken framing", fastcgitest.Raw([]byte("HTTP/1.1 400 Bad Request\r\n\r\n")), errFormatTx},
		{"rejected", fastcgitest.Raw(fastcgitest.End(1, 2)), "request rejected"},
		{"oversize", fastcgitest.Respond(200, strings.Repeat(" ", maxPage+1)), "response too large"},
		{"early close", fastcgitest.Raw(ok), "connection failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fastcgitest.NewTCP(t, tt.h)
			p, _ := newTestPlugin()
			e := fcgiEntry(s)
			obs := collect(t, p, urls(e))
			mustValueAt(t, obs, "", "phpfpm.pools_down", 1)
			mustPoolDown(t, obs, e, tt.want)
			mustNoAddress(t, obs, s.Address)
		})
	}
}

// TestFastCGIDialFailures: a socket that is missing, refuses or may not be
// opened says so; none of the texts names the path.
func TestFastCGIDialFailures(t *testing.T) {
	dir, err := os.MkdirTemp("", "fcgi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	missing := "unix:" + filepath.Join(dir, "missing.sock") + ":/status"

	// A socket file nobody listens on any more.
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()

	// A socket the agent may not open.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	lockedSock := filepath.Join(locked, "fpm.sock")
	ln2, err := net.Listen("unix", lockedSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln2.Close() })
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedTCP := "fcgi://" + tcp.Addr().String() + "/status"
	_ = tcp.Close()

	tests := []struct{ entry, want string }{
		{missing, "socket not found"},
		{"unix:" + stale + ":/status", "connection refused"},
		{refusedTCP, "connection refused"},
	}
	if os.Geteuid() != 0 { // root opens the socket anyway
		tests = append(tests, struct{ entry, want string }{"unix:" + lockedSock + ":/status", "permission denied"})
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			p, _ := newTestPlugin()
			obs := collect(t, p, urls(tt.entry))
			mustPoolDown(t, obs, tt.entry, tt.want)
			mustNoAddress(t, obs, dir)
		})
	}
}

// TestFastCGIIgnoresTheSecret: the basic-auth secret is for URL entries;
// a FastCGI pool is read with it named, and nothing of it is sent.
func TestFastCGIIgnoresTheSecret(t *testing.T) {
	s := fastcgitest.NewTCP(t, fastcgitest.Respond(200, page(1, 1, 0, 0, 1, 1)))
	p, _ := newTestPlugin()
	e := fcgiEntry(s)
	obs, err := p.CollectWithSecret(t.Context(), urls(e), map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
	if err != nil {
		t.Fatal(err)
	}
	mustValueAt(t, obs, e, "phpfpm.up", 1)
	for k, v := range s.Requests()[0].Params {
		if strings.Contains(k+v, plantUser) || strings.Contains(k+v, plantPass) {
			t.Fatalf("the secret was sent: %s=%s", k, v)
		}
	}
}

// TestFastCGIUnparsableEntry: an entry in a FastCGI form that does not
// parse (past the manager's validation only) is down and dials nothing.
func TestFastCGIUnparsableEntry(t *testing.T) {
	s := fastcgitest.NewTCP(t, fastcgitest.Respond(200, page(1, 1, 0, 0, 1, 1)))
	for _, e := range []string{
		"fcgi://" + s.Address + "/index.php",
		"fcgi://" + s.Address,
		"unix:relative.sock:/status",
	} {
		p, _ := newTestPlugin()
		obs := collect(t, p, urls(e))
		mustPoolDown(t, obs, e, "invalid address")
	}
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("%d requests sent for invalid entries", n)
	}
}

// TestFastCGIHungPoolTimesOut: a pool that never answers is down with
// "timed out" within the query timeout, and the round still reports the
// others.
func TestFastCGIHungPoolTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the query timeout")
	}
	hung := fastcgitest.NewTCP(t, nil)
	hung.SetHandler(func(fastcgitest.Request, net.Conn) { <-hung.Done() })
	good := fastcgitest.NewTCP(t, fastcgitest.Respond(200, page(1, 1, 0, 0, 1, 1)))
	p, _ := newTestPlugin()
	obs := collect(t, p, urls(fcgiEntry(hung), fcgiEntry(good)))
	mustValueAt(t, obs, "", "phpfpm.pools_down", 1)
	mustPoolDown(t, obs, fcgiEntry(hung), "timed out")
	mustValueAt(t, obs, fcgiEntry(good), "phpfpm.up", 1)
}
