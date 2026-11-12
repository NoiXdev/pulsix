package redis

import (
	"strings"
	"testing"
)

func TestParseSettingsDefaults(t *testing.T) {
	s, err := parseSettings(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := settings{network: "tcp", addr: "127.0.0.1:6379", host: "127.0.0.1", tls: "false"}
	if s != want {
		t.Fatalf("settings = %+v, want %+v", s, want)
	}
	s, err = parseSettings(map[string]string{})
	if err != nil || s != want {
		t.Fatalf("empty map: %+v, %v", s, err)
	}
}

func TestParseSettingsValues(t *testing.T) {
	s, err := parseSettings(map[string]string{"host": "db.example", "port": "6380", "user": "mon", "password": "pw", "tls": "skip-verify"})
	if err != nil {
		t.Fatal(err)
	}
	want := settings{network: "tcp", addr: "db.example:6380", host: "db.example", user: "mon", password: "pw", tls: "skip-verify"}
	if s != want {
		t.Fatalf("settings = %+v, want %+v", s, want)
	}
	s, err = parseSettings(map[string]string{"host": "::1"})
	if err != nil || s.addr != "[::1]:6379" {
		t.Fatalf("ipv6: %+v, %v", s, err)
	}
	s, err = parseSettings(map[string]string{"socket": "/run/redis.sock", "host": "ignored", "port": "1"})
	if err != nil || s.network != "unix" || s.addr != "/run/redis.sock" {
		t.Fatalf("socket: %+v, %v", s, err)
	}
	if _, err := parseSettings(map[string]string{"password": "pw"}); err != nil {
		t.Fatalf("password without user: %v", err)
	}
}

func TestParseSettingsErrorsNameTheFieldNeverTheValue(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"user without password", map[string]string{"user": plantedUser}, "secret field `password` is required"},
		{"bad port", map[string]string{"port": "70000"}, "secret field `port` is not a valid port"},
		{"port text", map[string]string{"port": plantedHost}, "secret field `port` is not a valid port"},
		{"bad tls", map[string]string{"tls": plantedHost}, "secret field `tls` is not one of false, true, skip-verify"},
		{"preferred is mysql's", map[string]string{"tls": "preferred"}, "secret field `tls` is not one of false, true, skip-verify"},
		{"NUL in host", map[string]string{"host": "a\x00b"}, "secret field `host` is not text without NUL bytes"},
		{"NUL in password", map[string]string{"password": "a\x00b"}, "secret field `password` is not text without NUL bytes"},
		{"NUL in socket", map[string]string{"socket": "a\x00b"}, "secret field `socket` is not text without NUL bytes"},
		{"tls on a socket", map[string]string{"socket": "/s", "tls": "true"}, "secret field `tls` is not false for a socket"},
		{"unknown field", map[string]string{"database": "x" + plantedPassword}, "secret field `database` is not known"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSettings(tc.fields)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			for _, v := range []string{plantedPassword, plantedHost, plantedUser} {
				if strings.Contains(err.Error(), v) {
					t.Fatalf("%q leaked into %q", v, err)
				}
			}
		})
	}
}

func TestIPv6HostWithAndWithoutBrackets(t *testing.T) {
	for _, host := range []string{"::1", "[::1]"} {
		s, err := parseSettings(map[string]string{"host": host, "port": "6380"})
		if err != nil {
			t.Fatal(err)
		}
		if s.addr != "[::1]:6380" || s.host != "::1" {
			t.Errorf("host %q: addr %q host %q", host, s.addr, s.host)
		}
	}
}
