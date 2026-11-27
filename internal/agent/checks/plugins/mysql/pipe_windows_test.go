//go:build windows

package mysql

import (
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// The pipe dialer reaches a server listening on a named pipe.
func TestDialPipe(t *testing.T) {
	name := `\\.\pipe\pulsix-mysql-test-` + strconv.FormatInt(time.Now().UnixNano(), 36)
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, "hello")
	}()
	s, err := parseSettings(map[string]string{"user": "u", "pipe": name})
	if err != nil {
		t.Fatal(err)
	}
	cfg := buildConfig(s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cfg.DialFunc(ctx, cfg.Net, cfg.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read %q, %v", buf, err)
	}
}
