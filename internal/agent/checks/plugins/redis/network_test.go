package redis

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A closed port reports "connection refused".
func TestRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	p, _ := newTestPlugin()
	obs := collect(t, p, map[string]string{"host": "127.0.0.1", "port": port, "user": plantedUser, "password": plantedPassword})
	mustDown(t, obs, "connection refused")
}

// A server that accepts TCP and never answers (not even AUTH) must not hold
// a round past the plugin's deadline, and the connection must be closed.
func TestHungServerEndsWithinTheDeadline(t *testing.T) {
	tcfg := tlsConfig(t)
	for _, mode := range []string{"false", "true", "skip-verify"} {
		for _, hangAfterTLS := range []bool{false, true} {
			// "true" refuses the test certificate before anything can hang.
			if (mode == "false" || mode == "true") && hangAfterTLS {
				continue
			}
			t.Run(mode+"/tls-server="+strconv.FormatBool(hangAfterTLS), func(t *testing.T) {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				if hangAfterTLS {
					l = tlsListener(l, tcfg)
				}
				defer func() { _ = l.Close() }()
				var accepted, closed atomic.Int32
				go func() {
					for {
						c, err := l.Accept()
						if err != nil {
							return
						}
						accepted.Add(1)
						go func() {
							buf := make([]byte, 1024)
							for {
								if _, err := c.Read(buf); err != nil {
									closed.Add(1)
									return
								}
							}
						}()
					}
				}()
				p, _ := newTestPlugin()
				p.timeout = 400 * time.Millisecond
				start := time.Now()
				obs, cerr := p.CollectWithSecret(context.Background(), nil, map[string]string{
					"host": "127.0.0.1", "port": strconv.Itoa(l.Addr().(*net.TCPAddr).Port),
					"password": plantedPassword, "tls": mode,
				})
				if took := time.Since(start); took > 3*time.Second {
					t.Fatalf("took %v", took)
				}
				if cerr != nil {
					t.Fatal(cerr)
				}
				noSecretIn(t, obs, cerr)
				mustDown(t, obs, "timed out")
				deadline := time.Now().Add(2 * time.Second)
				for accepted.Load() != closed.Load() && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if accepted.Load() == 0 || accepted.Load() != closed.Load() {
					t.Errorf("accepted %d, closed %d: a connection was left open", accepted.Load(), closed.Load())
				}
			})
		}
	}
}

// A server that closes at once, or in the middle of a reply, is a failed
// connection.
func TestServerThatHangsUp(t *testing.T) {
	t.Run("at once", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		p, _ := newTestPlugin()
		obs := collect(t, p, map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(l.Addr().(*net.TCPAddr).Port)})
		mustDown(t, obs, "connection failed")
	})
	t.Run("mid-reply", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		go func() {
			c, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			_, _ = c.Read(buf)
			_, _ = c.Write([]byte("$100\r\nshort"))
			_ = c.Close()
		}()
		p, _ := newTestPlugin()
		obs := collect(t, p, map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(l.Addr().(*net.TCPAddr).Port)})
		mustDown(t, obs, "connection failed")
	})
}

func TestCallerCancelsMidRound(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { buf := make([]byte, 64); _, _ = c.Read(buf); _, _ = c.Read(buf) }()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	p, _ := newTestPlugin()
	start := time.Now()
	obs, cerr := p.CollectWithSecret(ctx, nil, map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(l.Addr().(*net.TCPAddr).Port)})
	if obs != nil || cerr == nil || ctx.Err() == nil {
		t.Fatalf("obs = %+v, err = %v", obs, cerr)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestTLS(t *testing.T) {
	tcfg := tlsConfig(t)
	t.Run("an unknown certificate authority is refused", func(t *testing.T) {
		s := serve(t, tcfg, bulk(primaryInfo()))
		p, _ := newTestPlugin()
		mustDown(t, collect(t, p, fieldsFor(s, map[string]string{"tls": "true", "host": "localhost"})), "TLS handshake failed")
	})
	t.Run("skip-verify accepts it", func(t *testing.T) {
		s := serve(t, tcfg, "+OK\r\n", bulk(primaryInfo()))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, map[string]string{"tls": "skip-verify", "password": plantedPassword}))
		mustValue(t, obs, "redis.up", 1)
		if got, want := s.received(), cmd("AUTH", plantedPassword)+cmd("INFO"); got != want {
			t.Fatalf("sent %q", got)
		}
	})
	t.Run("TLS against a plain server", func(t *testing.T) {
		s := serve(t, nil, bulk(primaryInfo()))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, map[string]string{"tls": "true"}))
		mustValue(t, obs, "redis.up", 0)
	})
	t.Run("plain against a TLS server", func(t *testing.T) {
		s := serve(t, tcfg, bulk(primaryInfo()))
		p, _ := newTestPlugin()
		obs := collect(t, p, fieldsFor(s, nil))
		mustValue(t, obs, "redis.up", 0)
	})
}

func TestUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are exercised on Linux and macOS")
	}
	dir, err := os.MkdirTemp("", "rd")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "r.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	defer func() { _ = l.Close() }()
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 256)
		n, _ := c.Read(buf)
		got <- string(buf[:n])
		_, _ = c.Write([]byte(bulk(primaryInfo())))
		_, _ = c.Read(buf)
	}()
	p, _ := newTestPlugin()
	// host and port are not used when a socket is set
	obs := collect(t, p, map[string]string{"socket": path, "host": plantedHost, "port": "1"})
	mustValue(t, obs, "redis.up", 1)
	if g := <-got; g != cmd("INFO") {
		t.Fatalf("sent %q", g)
	}
}
