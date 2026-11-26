package redis

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// plantedPassword, plantedUser and plantedHost are values the tests plant in
// the secret and in server replies; no observation or advisory error may
// carry them.
const (
	plantedPassword = "p4ss-w0rd-SECRET"
	plantedUser     = "planted-user-name"
	plantedHost     = "planted-host.example"
)

// bulk is a RESP bulk string reply.
func bulk(s string) string { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }

// info renders an INFO text from key/value pairs in a stable order.
func info(pairs ...string) string {
	var b strings.Builder
	b.WriteString("# Server\r\nredis_version:7.4.1\r\n\r\n# Memory\r\n")
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString(pairs[i] + ":" + pairs[i+1] + "\r\n")
	}
	return b.String()
}

// primaryInfo is a healthy primary with 100 MiB used of a 1 GiB maxmemory
// that saved a snapshot a minute ago and has AOF off.
func primaryInfo(extra ...string) string {
	base := []string{
		"used_memory", "104857600", "maxmemory", "1073741824", "total_system_memory", "8589934592",
		"connected_clients", "7", "role", "master", "uptime_in_seconds", "1000", "evicted_keys", "10",
		// Persistence: last saved 60 s before the test clock's 1_000_000.
		"loading", "0", "rdb_changes_since_last_save", "12", "rdb_last_save_time", "999940",
		"rdb_last_bgsave_status", "ok", "aof_enabled", "0", "aof_last_bgrewrite_status", "ok",
		"aof_last_write_status", "ok",
	}
	return info(overlay(base, extra)...)
}

// overlay returns base with the pairs in extra replacing or adding keys. An
// extra value of "-" removes the key.
func overlay(base, extra []string) []string {
	m := map[string]string{}
	var order []string
	for _, kv := range [][]string{base, extra} {
		for i := 0; i+1 < len(kv); i += 2 {
			if _, ok := m[kv[i]]; !ok {
				order = append(order, kv[i])
			}
			m[kv[i]] = kv[i+1]
		}
	}
	var out []string
	for _, k := range order {
		if m[k] != "-" {
			out = append(out, k, m[k])
		}
	}
	return out
}

// server is an in-process fake Redis: it reads one command per scripted
// reply and answers with that reply, recording the raw bytes it received.
type server struct {
	l  net.Listener
	mu sync.Mutex
	rx strings.Builder
}

func (s *server) received() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rx.String()
}

func (s *server) port() string { return strconv.Itoa(s.l.Addr().(*net.TCPAddr).Port) }
func (s *server) addr() string { return s.l.Addr().String() }

// readCommand reads one RESP array of bulk strings and records its bytes.
func (s *server) readCommand(r *bufio.Reader) error {
	var raw strings.Builder
	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	raw.WriteString(line)
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return err
	}
	for range n {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		raw.WriteString(hdr)
		size, err := strconv.Atoi(strings.TrimSpace(hdr[1:]))
		if err != nil {
			return err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		raw.Write(buf)
	}
	s.mu.Lock()
	s.rx.WriteString(raw.String())
	s.mu.Unlock()
	return nil
}

// serve starts a fake server on 127.0.0.1 answering each connection's
// commands with replies in order, then closing the connection. cfg, when
// set, wraps the connections in TLS.
func serve(t *testing.T, cfg *tls.Config, replies ...string) *server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		l = tls.NewListener(l, cfg)
	}
	s := &server{l: l}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				r := bufio.NewReader(c)
				for _, reply := range replies {
					if s.readCommand(r) != nil {
						return
					}
					if _, err := io.WriteString(c, reply); err != nil {
						return
					}
				}
				// Let the client read the last reply before the close.
				_, _ = r.Peek(1)
			}()
		}
	}()
	return s
}

// tlsConfig returns a server TLS configuration with a self-signed
// certificate valid for 127.0.0.1, harvested from httptest.
func tlsConfig(t *testing.T) *tls.Config {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	return &tls.Config{Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12}
}

// newTestPlugin returns a plugin with a fake clock.
func newTestPlugin() (*plugin, *clock) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	return &plugin{now: c.now}, c
}

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// fieldsFor are the secret fields that point at s.
func fieldsFor(s *server, extra map[string]string) map[string]string {
	f := map[string]string{"host": "127.0.0.1", "port": s.port()}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

// find returns the observation of item/metric.
func find(obs []checks.Observation, item, metric string) (checks.Observation, bool) {
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o, true
		}
	}
	return checks.Observation{}, false
}

func mustValue(t *testing.T, obs []checks.Observation, metric string, want float64) {
	t.Helper()
	o, ok := find(obs, "", metric)
	if !ok {
		t.Fatalf("no observation %q in %+v", metric, obs)
	}
	if o.Value == nil || *o.Value != want || o.Err != "" {
		t.Fatalf("%q = %+v, want value %v", metric, o, want)
	}
}

func mustLabel(t *testing.T, obs []checks.Observation, metric, want string) {
	t.Helper()
	o, ok := find(obs, "", metric)
	if !ok {
		t.Fatalf("no observation %q in %+v", metric, obs)
	}
	if o.Label == nil || *o.Label != want || o.Value != nil || o.Err != "" {
		t.Fatalf("%q = %+v, want label %q", metric, o, want)
	}
}

func mustErr(t *testing.T, obs []checks.Observation, metric, want string) {
	t.Helper()
	o, ok := find(obs, "", metric)
	if !ok {
		t.Fatalf("no observation %q in %+v", metric, obs)
	}
	if o.Value != nil || o.Label != nil || o.Err != want {
		t.Fatalf("%q = %+v, want error %q", metric, o, want)
	}
}

func mustAbsent(t *testing.T, obs []checks.Observation, metric string) {
	t.Helper()
	if o, ok := find(obs, "", metric); ok {
		t.Fatalf("%q reported: %+v", metric, o)
	}
}

// mustDown asserts the whole down set: up 0 and the reason on every other
// metric.
func mustDown(t *testing.T, obs []checks.Observation, reason string) {
	t.Helper()
	mustValue(t, obs, "redis.up", 0)
	if len(obs) != 1+len(hostMetrics) {
		t.Fatalf("got %d observations, want %d: %+v", len(obs), 1+len(hostMetrics), obs)
	}
	for _, m := range hostMetrics {
		mustErr(t, obs, m, "redis: "+reason)
	}
}

func noSecretIn(t *testing.T, obs []checks.Observation, err error) {
	t.Helper()
	var all []string
	for _, o := range obs {
		all = append(all, o.Item, o.Metric, o.Err)
		if o.Label != nil {
			all = append(all, *o.Label)
		}
	}
	if err != nil {
		all = append(all, err.Error())
	}
	text := strings.Join(all, "\n")
	for _, s := range []string{plantedPassword, plantedUser, plantedHost} {
		if strings.Contains(text, s) {
			t.Fatalf("%q leaked into %q", s, text)
		}
	}
}

// cmd renders a command as the RESP array a client sends.
func cmd(args ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	return b.String()
}

func tlsListener(l net.Listener, cfg *tls.Config) net.Listener { return tls.NewListener(l, cfg) }
