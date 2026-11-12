package postgres

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func localFields(port string, extra map[string]string) map[string]string {
	f := map[string]string{"host": "127.0.0.1", "port": port, "user": plantedUser, "password": plantedPassword}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

// A closed port reports "connection refused" through the real driver.
func TestRefusedThroughTheRealDriver(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	obs, cerr := New().(plugin).CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"sslmode": "disable"}))
	if cerr != nil {
		t.Fatal(cerr)
	}
	mustValue(t, obs, "", "pg.up", 0)
	mustErr(t, obs, "", "pg.connections", "postgres: connection refused")
	noSecretIn(t, obs, cerr)
}

// A server that accepts TCP and never answers must not hold a round past
// the plugin's deadline, whatever the TLS negotiation (review focus 2).
func TestHungServerEndsWithinTheDeadline(t *testing.T) {
	for _, mode := range []string{"disable", "prefer", "require"} {
		t.Run(mode, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
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
			port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
			p := New().(plugin)
			p.timeout = 400 * time.Millisecond
			start := time.Now()
			obs, cerr := p.CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"sslmode": mode}))
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("took %v", took)
			}
			if cerr != nil {
				t.Fatal(cerr)
			}
			mustValue(t, obs, "", "pg.up", 0)
			mustErr(t, obs, "", "pg.connections", "postgres: timed out")
			noSecretIn(t, obs, cerr)
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

// A server that closes at once is a plain failed connection.
func TestServerThatHangsUpIsAConnectionFailure(t *testing.T) {
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
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	obs, cerr := New().(plugin).CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"sslmode": "disable"}))
	if cerr != nil {
		t.Fatal(cerr)
	}
	mustValue(t, obs, "", "pg.up", 0)
	noSecretIn(t, obs, cerr)
}
