package mysql

import (
	"github.com/go-sql-driver/mysql"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

func TestParseSettingsDefaults(t *testing.T) {
	s, err := parseSettings(map[string]string{"user": "mon"})
	if err != nil {
		t.Fatal(err)
	}
	want := connSettings{host: "127.0.0.1", port: "3306", user: "mon", tls: "preferred"}
	if s != want {
		t.Fatalf("settings = %+v, want %+v", s, want)
	}
}

func TestParseSettingsErrorsNameTheFieldNeverTheValue(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"missing user", map[string]string{"password": plantedPassword}, "secret field `user` is required"},
		{"bad port", map[string]string{"user": "u", "port": "70000"}, "secret field `port` is not a valid port"},
		{"port text", map[string]string{"user": "u", "port": plantedHost}, "secret field `port` is not a valid port"},
		{"bad tls", map[string]string{"user": "u", "tls": plantedHost}, "secret field `tls` is not one of false, preferred, true, skip-verify"},
		{"NUL in host", map[string]string{"user": "u", "host": "a\x00b"}, "secret field `host` is not text without NUL bytes"},
		{"NUL in password", map[string]string{"user": "u", "password": "a\x00b"}, "secret field `password` is not text without NUL bytes"},
		{"unknown field", map[string]string{"user": "u", "sslmode": "x" + plantedPassword}, "secret field `sslmode` is not known"},
		{"another database's field", map[string]string{"user": "u", "database": "d"}, "secret field `database` is not known"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSettings(tc.fields)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), plantedPassword) || strings.Contains(err.Error(), plantedHost) {
				t.Fatalf("value leaked into %q", err)
			}
		})
	}
}

func TestParseSettingsAcceptsEveryTLSMode(t *testing.T) {
	for _, mode := range []string{"false", "preferred", "true", "skip-verify"} {
		s, err := parseSettings(map[string]string{"user": "u", "tls": mode})
		if err != nil || s.tls != mode {
			t.Errorf("tls=%s: %+v, %v", mode, s, err)
		}
	}
}

func TestBuildConfigTCP(t *testing.T) {
	// A hostile-looking value is a value, not syntax.
	s := connSettings{host: "db.example", port: "3307", user: "u@h/x?y=z", password: "p/w?allowAllFiles=true&x=(", tls: "true"}
	cfg := buildConfig(s)
	if cfg.Net != "tcp" || cfg.Addr != "db.example:3307" {
		t.Errorf("net %q addr %q", cfg.Net, cfg.Addr)
	}
	if cfg.User != s.user || cfg.Passwd != s.password {
		t.Errorf("credentials were altered: %q %q", cfg.User, cfg.Passwd)
	}
	// TLS is a tls.Config built by withTLS, never a name in the driver's
	// global registry.
	if cfg.TLSConfig != "" || cfg.DBName != "" || len(cfg.Params) != 0 {
		t.Errorf("tls %q db %q params %v", cfg.TLSConfig, cfg.DBName, cfg.Params)
	}
	if cfg.AllowCleartextPasswords || cfg.AllowOldPasswords || cfg.AllowAllFiles || cfg.ParseTime || !cfg.AllowNativePasswords {
		t.Errorf("unsafe or unexpected switches: %+v", cfg)
	}
	if cfg.Timeout != dbcheck.CollectTimeout || cfg.ReadTimeout != 0 || cfg.WriteTimeout != 0 {
		t.Errorf("timeouts %v %v %v", cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout)
	}
	if cfg.Timeout > 10*time.Second {
		t.Errorf("dial timeout %v exceeds the round", cfg.Timeout)
	}
}

func TestBuildConfigIPv6AndSocket(t *testing.T) {
	if cfg := buildConfig(connSettings{host: "::1", port: "3306"}); cfg.Addr != "[::1]:3306" {
		t.Errorf("addr %q", cfg.Addr)
	}
	cfg := buildConfig(connSettings{host: "ignored", port: "1", socket: "/run/mysqld/mysqld.sock"})
	if cfg.Net != "unix" || cfg.Addr != "/run/mysqld/mysqld.sock" {
		t.Errorf("net %q addr %q", cfg.Net, cfg.Addr)
	}
}

// The driver's logger writes to stderr by default; the plugin's config
// must carry a silent one.
func TestBuildConfigSilencesTheDriverLogger(t *testing.T) {
	cfg := buildConfig(connSettings{host: "h", port: "1"})
	if _, ok := cfg.Logger.(*mysql.NopLogger); !ok {
		t.Fatalf("logger is %T, want *mysql.NopLogger: the driver's default writes to stderr", cfg.Logger)
	}
	cfg.Logger.Print("must not print anywhere")
}

func TestHostBracketsAreStripped(t *testing.T) {
	for host, want := range map[string]string{
		"[::1]": "[::1]:3306", "::1": "[::1]:3306", "db.local": "db.local:3306", "127.0.0.1": "127.0.0.1:3306",
	} {
		if got := buildConfig(connSettings{host: host, port: "3306"}).Addr; got != want {
			t.Errorf("host %q: addr %q, want %q", host, got, want)
		}
	}
}
