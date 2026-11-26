package mysql

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
)

func absPath() string {
	if runtime.GOOS == "windows" {
		return `C:\pulsix\x.pem`
	}
	return "/etc/pulsix/x.pem"
}

func TestParseSettingsTLSFieldRules(t *testing.T) {
	abs := absPath()
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"CA with preferred", map[string]string{"user": "u", "tls_ca": abs}, "secret field `tls_ca` needs `tls` true"},
		{"CA with skip-verify", map[string]string{"user": "u", "tls": "skip-verify", "tls_ca": abs}, "secret field `tls_ca` needs `tls` true"},
		{"server name with preferred", map[string]string{"user": "u", "tls_server_name": "db"}, "secret field `tls_server_name` needs `tls` true"},
		{"client certificate without TLS", map[string]string{"user": "u", "tls": "false", "tls_cert": abs, "tls_key": abs}, "secret field `tls_cert` needs `tls` other than false"},
		{"client key without TLS", map[string]string{"user": "u", "tls": "false", "tls_key": abs}, "secret field `tls_key` needs `tls` other than false"},
		{"relative CA", map[string]string{"user": "u", "tls": "true", "tls_ca": plantedHost + ".pem"}, "secret field `tls_ca` is not an absolute path"},
		{"relative cert", map[string]string{"user": "u", "tls_cert": plantedHost + ".pem"}, "secret field `tls_cert` is not an absolute path"},
		{"relative key", map[string]string{"user": "u", "tls_key": plantedHost + ".key"}, "secret field `tls_key` is not an absolute path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSettings(tc.fields)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	s, err := parseSettings(map[string]string{"user": "u", "tls": "true", "tls_ca": abs, "tls_cert": abs, "tls_key": abs, "tls_server_name": "db.internal"})
	if err != nil || s.tlsCA != abs || s.tlsCert != abs || s.tlsKey != abs || s.serverName != "db.internal" {
		t.Fatalf("%+v, %v", s, err)
	}
	for _, mode := range []string{"preferred", "skip-verify"} {
		if _, err := parseSettings(map[string]string{"user": "u", "tls": mode, "tls_cert": abs, "tls_key": abs}); err != nil {
			t.Errorf("%s with a client certificate: %v", mode, err)
		}
	}
}

func TestTLSConfig(t *testing.T) {
	dir := t.TempDir()
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	caPath := testpki.Write(t, dir, "ca.pem", ca.PEM, 0o644)
	cert := testpki.Write(t, dir, "c.pem", client.CertPEM, 0o644)
	key := testpki.Write(t, dir, "c.key", client.KeyPEM, 0o600)
	base := connSettings{host: "[::1]", port: "3306", user: "u"}

	with := func(t *testing.T, s connSettings) connSettings {
		t.Helper()
		out, err := s.withTLS()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("false is plain", func(t *testing.T) {
		s := base
		s.tls = "false"
		cfg := buildConfig(with(t, s))
		if cfg.TLS != nil || cfg.TLSConfig != "" || cfg.AllowFallbackToPlaintext {
			t.Fatalf("TLS %+v name %q fallback %v", cfg.TLS, cfg.TLSConfig, cfg.AllowFallbackToPlaintext)
		}
	})
	t.Run("preferred tries TLS without verification and falls back", func(t *testing.T) {
		s := base
		s.tls, s.tlsCert, s.tlsKey = "preferred", cert, key
		cfg := buildConfig(with(t, s))
		if cfg.TLS == nil || !cfg.TLS.InsecureSkipVerify || !cfg.AllowFallbackToPlaintext || len(cfg.TLS.Certificates) != 1 {
			t.Fatalf("TLS %+v fallback %v", cfg.TLS, cfg.AllowFallbackToPlaintext)
		}
	})
	t.Run("skip-verify does not fall back", func(t *testing.T) {
		s := base
		s.tls = "skip-verify"
		cfg := buildConfig(with(t, s))
		if cfg.TLS == nil || !cfg.TLS.InsecureSkipVerify || cfg.AllowFallbackToPlaintext || len(cfg.TLS.Certificates) != 0 {
			t.Fatalf("TLS %+v fallback %v", cfg.TLS, cfg.AllowFallbackToPlaintext)
		}
	})
	t.Run("true verifies against the system roots and the host", func(t *testing.T) {
		s := base
		s.tls = "true"
		cfg := buildConfig(with(t, s))
		if cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.RootCAs != nil || cfg.TLS.ServerName != "::1" || cfg.AllowFallbackToPlaintext {
			t.Fatalf("TLS %+v", cfg.TLS)
		}
	})
	t.Run("true with a CA file, a server name and a client certificate", func(t *testing.T) {
		s := base
		s.tls, s.tlsCA, s.serverName, s.tlsCert, s.tlsKey = "true", caPath, "db.internal", cert, key
		cfg := buildConfig(with(t, s))
		if cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || !cfg.TLS.RootCAs.Equal(ca.Pool()) || cfg.TLS.ServerName != "db.internal" || len(cfg.TLS.Certificates) != 1 {
			t.Fatalf("TLS %+v", cfg.TLS)
		}
	})
	t.Run("true over a socket verifies against the host field", func(t *testing.T) {
		s := base
		s.tls, s.socket, s.host = "true", "/run/mysqld/mysqld.sock", "127.0.0.1"
		cfg := buildConfig(with(t, s))
		if cfg.TLS == nil || cfg.TLS.ServerName != "127.0.0.1" {
			t.Fatalf("TLS %+v", cfg.TLS)
		}
	})
	t.Run("a file error names the field", func(t *testing.T) {
		s := base
		s.tls, s.tlsCA = "true", filepath.Join(dir, plantedHost+".pem")
		obs, err := collect(t, newTestPlugin(&fakeSession{}, nil, nil), map[string]string{"user": "u", "tls": "true", "tls_ca": s.tlsCA})
		mustErr(t, obs, "", "mysql.up", "mysql: secret field `tls_ca` cannot be read")
		noSecretIn(t, obs, err)
	})
}
