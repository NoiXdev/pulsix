package mysql

import (
	"context"
	"runtime"
	"testing"
)

func TestPipePath(t *testing.T) {
	for in, want := range map[string]string{
		"MySQL":              `\\.\pipe\MySQL`,
		`\\.\pipe\MySQL`:     `\\.\pipe\MySQL`,
		`\\.\PIPE\mysql 8.4`: `\\.\pipe\mysql 8.4`,
		"a/b":                `\\.\pipe\a/b`,
	} {
		if got, ok := pipePath(in); !ok || got != want {
			t.Errorf("pipePath(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'p'
	}
	for _, in := range []string{"", `\\.\pipe\`, `\\db.example\pipe\MySQL`, `pipe\MySQL`, `\\.\pipe\a\b`, "a\x00b", string(long)} {
		if got, ok := pipePath(in); ok {
			t.Errorf("pipePath(%q) = %q, accepted", in, got)
		}
	}
}

func TestParseSettingsPipe(t *testing.T) {
	_, err := parseSettings(map[string]string{"user": "u", "pipe": `\\` + plantedHost + `\pipe\MySQL`})
	if err == nil || err.Error() != "secret field `pipe` is not a local pipe name such as MySQL or \\\\.\\pipe\\MySQL" {
		t.Fatalf("remote pipe: %v", err)
	}
	_, err = parseSettings(map[string]string{"user": "u", "pipe": "MySQL", "socket": "/s"})
	if err == nil || err.Error() != "secret field `pipe` cannot be combined with `socket`" {
		t.Fatalf("pipe and socket: %v", err)
	}
	s, err := parseSettings(map[string]string{"user": "u", "pipe": "MySQL", "host": "ignored", "port": "1"})
	if runtime.GOOS != "windows" {
		if err == nil || err.Error() != "secret field `pipe` is only available on Windows" {
			t.Fatalf("pipe on %s: %+v, %v", runtime.GOOS, s, err)
		}
		return
	}
	if err != nil || s.pipe != `\\.\pipe\MySQL` {
		t.Fatalf("pipe: %+v, %v", s, err)
	}
}

// The dialer is chosen by the settings: a pipe gets the pipe dialer, TCP and
// sockets the driver's own.
func TestBuildConfigPipe(t *testing.T) {
	cfg := buildConfig(connSettings{host: "ignored", port: "1", pipe: `\\.\pipe\MySQL`, user: "u"})
	if cfg.Net != "pipe" || cfg.Addr != `\\.\pipe\MySQL` || cfg.DialFunc == nil {
		t.Fatalf("net %q addr %q dial %v", cfg.Net, cfg.Addr, cfg.DialFunc != nil)
	}
	if runtime.GOOS != "windows" {
		if _, err := cfg.DialFunc(context.Background(), cfg.Net, cfg.Addr); err == nil {
			t.Fatal("a pipe dialed outside Windows")
		}
	}
	for _, s := range []connSettings{{host: "h", port: "1"}, {socket: "/run/mysqld/mysqld.sock"}} {
		if cfg := buildConfig(s); cfg.DialFunc != nil {
			t.Errorf("%+v: a custom dialer for %s", s, cfg.Net)
		}
	}
}
