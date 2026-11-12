package mysql

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
	obs, cerr := New().(*plugin).CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"tls": "false"}))
	if cerr != nil {
		t.Fatal(cerr)
	}
	mustValue(t, obs, "", "mysql.up", 0)
	mustErr(t, obs, "", "mysql.connections", "mysql: connection refused")
	noSecretIn(t, obs, cerr)
}

// A server that accepts TCP and never answers must not hold a round past
// the plugin's deadline, whatever the TLS setting.
func TestHungServerEndsWithinTheDeadline(t *testing.T) {
	for _, mode := range []string{"false", "preferred", "true", "skip-verify"} {
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
			p := New().(*plugin)
			p.timeout = 400 * time.Millisecond
			start := time.Now()
			obs, cerr := p.CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"tls": mode}))
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("took %v", took)
			}
			if cerr != nil {
				t.Fatal(cerr)
			}
			mustValue(t, obs, "", "mysql.up", 0)
			mustErr(t, obs, "", "mysql.connections", "mysql: timed out")
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
	obs, cerr := New().(*plugin).CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"tls": "false"}))
	if cerr != nil {
		t.Fatal(cerr)
	}
	mustValue(t, obs, "", "mysql.up", 0)
	mustErr(t, obs, "", "mysql.connections", "mysql: connection failed")
	noSecretIn(t, obs, cerr)
}

// fakeMySQL accepts connections, speaks just enough of the handshake to log
// in with any password, and then reads and never answers a statement. It
// counts the connections it accepted and the ones that ended.
func fakeMySQL(t *testing.T, accepted, closed *atomic.Int32) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	packet := func(seq byte, payload []byte) []byte {
		return append([]byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}, payload...)
	}
	greeting := []byte{10}
	greeting = append(greeting, "5.7.0\x00"...)
	greeting = append(greeting, 1, 0, 0, 0)                                     // connection id
	greeting = append(greeting, "abcdefgh"...)                                  // auth data, part 1
	greeting = append(greeting, 0, 0x00, 0x82, 33, 2, 0, 0x08, 0, 21)           // filler, caps low, charset, status, caps high, auth data length
	greeting = append(greeting, make([]byte, 10)...)                            // reserved
	greeting = append(greeting, "ijklmnopqrst\x00mysql_native_password\x00"...) // part 2, plugin
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer closed.Add(1)
				defer func() { _ = c.Close() }()
				if _, err := c.Write(packet(0, greeting)); err != nil {
					return
				}
				buf := make([]byte, 4096)
				if _, err := c.Read(buf); err != nil { // the login
					return
				}
				if _, err := c.Write(packet(2, []byte{0, 0, 0, 2, 0, 0, 0})); err != nil {
					return
				}
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// A statement that outlasts the query timeout but not the round is a
// timeout, not a failed connection, and its connection is closed.
func TestHungQueryIsATimeoutNotAFailedConnection(t *testing.T) {
	var accepted, closed atomic.Int32
	port := fakeMySQL(t, &accepted, &closed)
	p := New().(*plugin)
	p.queryTimeout = 300 * time.Millisecond
	p.timeout = 10 * time.Second
	start := time.Now()
	obs, cerr := p.CollectWithSecret(context.Background(), nil, localFields(port, map[string]string{"tls": "false"}))
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v: the round ended it, not the query timeout", took)
	}
	if cerr != nil {
		t.Fatal(cerr)
	}
	mustValue(t, obs, "", "mysql.up", 0)
	mustErr(t, obs, "", "mysql.connections", "mysql: timed out")
	noSecretIn(t, obs, cerr)
	deadline := time.Now().Add(2 * time.Second)
	for accepted.Load() != closed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if accepted.Load() != 1 || closed.Load() != 1 {
		t.Errorf("accepted %d, closed %d", accepted.Load(), closed.Load())
	}
}
