package redis

import (
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
)

func TestParseSettingsTLSFieldRules(t *testing.T) {
	abs := "/etc/pulsix/x.pem"
	if runtime.GOOS == "windows" {
		abs = `C:\pulsix\x.pem`
	}
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"CA without verification", map[string]string{"tls": "skip-verify", "tls_ca": abs}, "secret field `tls_ca` needs `tls` true"},
		{"CA without TLS", map[string]string{"tls_ca": abs}, "secret field `tls_ca` needs `tls` true"},
		{"server name without verification", map[string]string{"tls": "skip-verify", "tls_server_name": "r"}, "secret field `tls_server_name` needs `tls` true"},
		{"client certificate without TLS", map[string]string{"tls_cert": abs, "tls_key": abs}, "secret field `tls_cert` needs `tls` true or skip-verify"},
		{"client key without TLS", map[string]string{"tls_key": abs}, "secret field `tls_key` needs `tls` true or skip-verify"},
		{"relative CA", map[string]string{"tls": "true", "tls_ca": plantedHost + ".pem"}, "secret field `tls_ca` is not an absolute path"},
		{"relative cert", map[string]string{"tls": "true", "tls_cert": plantedHost + ".pem"}, "secret field `tls_cert` is not an absolute path"},
		{"relative key", map[string]string{"tls": "true", "tls_key": plantedHost + ".key"}, "secret field `tls_key` is not an absolute path"},
		{"NUL in server name", map[string]string{"tls": "true", "tls_server_name": "a\x00b"}, "secret field `tls_server_name` is not text without NUL bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSettings(tc.fields)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	s, err := parseSettings(map[string]string{"tls": "true", "tls_ca": abs, "tls_cert": abs, "tls_key": abs, "tls_server_name": "r.internal"})
	if err != nil || s.tlsCA != abs || s.tlsCert != abs || s.tlsKey != abs || s.serverName != "r.internal" {
		t.Fatalf("%+v, %v", s, err)
	}
	if _, err := parseSettings(map[string]string{"tls": "skip-verify", "tls_cert": abs, "tls_key": abs}); err != nil {
		t.Fatalf("skip-verify with a client certificate: %v", err)
	}
}

// pki is a CA with a server certificate for 127.0.0.1 and redis.internal,
// and the CA file written to disk.
type pki struct {
	ca     *testpki.CA
	dir    string
	caPath string
	server tls.Certificate
}

func newPKI(t *testing.T, names ...string) pki {
	t.Helper()
	ca := testpki.NewCA(t)
	dir := t.TempDir()
	if len(names) == 0 {
		names = []string{"127.0.0.1", "redis.internal"}
	}
	return pki{ca: ca, dir: dir, caPath: testpki.Write(t, dir, "ca.pem", ca.PEM, 0o644), server: ca.Server(t, names...).TLS(t)}
}

func (p pki) serverConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{p.server}, MinVersion: tls.VersionTLS12}
}

// client writes a client certificate for cn and returns its paths.
func (p pki) client(t *testing.T, cn string) (cert, key string) {
	t.Helper()
	c := p.ca.Client(t, cn)
	return testpki.Write(t, p.dir, cn+".pem", c.CertPEM, 0o644), testpki.Write(t, p.dir, cn+".key", c.KeyPEM, 0o600)
}

func TestTLSWithACustomCA(t *testing.T) {
	p := newPKI(t)
	t.Run("verified against the CA file", func(t *testing.T) {
		s := serve(t, p.serverConfig(), bulk(primaryInfo()))
		pl, _ := newTestPlugin()
		mustValue(t, collect(t, pl, fieldsFor(s, map[string]string{"tls": "true", "tls_ca": p.caPath})), "redis.up", 1)
	})
	t.Run("the system roots do not know the CA", func(t *testing.T) {
		s := serve(t, p.serverConfig(), bulk(primaryInfo()))
		pl, _ := newTestPlugin()
		mustDown(t, collect(t, pl, fieldsFor(s, map[string]string{"tls": "true"})), "TLS handshake failed")
	})
	t.Run("another CA", func(t *testing.T) {
		s := serve(t, p.serverConfig(), bulk(primaryInfo()))
		pl, _ := newTestPlugin()
		other := testpki.Write(t, p.dir, "other.pem", testpki.NewCA(t).PEM, 0o644)
		mustDown(t, collect(t, pl, fieldsFor(s, map[string]string{"tls": "true", "tls_ca": other})), "TLS handshake failed")
	})
	t.Run("an unreadable CA file is named without the path", func(t *testing.T) {
		pl, _ := newTestPlugin()
		obs, err := pl.CollectWithSecret(t.Context(), nil, map[string]string{"tls": "true", "tls_ca": filepath.Join(p.dir, plantedHost+".pem")})
		if len(obs) != 1 {
			t.Fatalf("obs = %+v", obs)
		}
		mustErr(t, obs, "redis.up", "redis: secret field `tls_ca` cannot be read")
		noSecretIn(t, obs, err)
	})
}

func TestTLSServerName(t *testing.T) {
	p := newPKI(t, "redis.internal")
	t.Run("the host does not match the certificate", func(t *testing.T) {
		s := serve(t, p.serverConfig(), bulk(primaryInfo()))
		pl, _ := newTestPlugin()
		mustDown(t, collect(t, pl, fieldsFor(s, map[string]string{"tls": "true", "tls_ca": p.caPath})), "TLS handshake failed")
	})
	t.Run("tls_server_name names it", func(t *testing.T) {
		s := serve(t, p.serverConfig(), bulk(primaryInfo()))
		pl, _ := newTestPlugin()
		f := fieldsFor(s, map[string]string{"tls": "true", "tls_ca": p.caPath, "tls_server_name": "redis.internal"})
		mustValue(t, collect(t, pl, f), "redis.up", 1)
	})
}

func TestTLSClientCertificate(t *testing.T) {
	p := newPKI(t)
	cert, key := p.client(t, "monitor")
	var mu sync.Mutex
	var seen []string
	cfg := p.serverConfig()
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.ClientCAs = p.ca.Pool()
	cfg.VerifyPeerCertificate = func(_ [][]byte, chains [][]*x509.Certificate) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, chains[0][0].Subject.CommonName)
		return nil
	}
	for _, mode := range []string{"true", "skip-verify"} {
		t.Run("sent with tls "+mode, func(t *testing.T) {
			s := serve(t, cfg, bulk(primaryInfo()))
			pl, _ := newTestPlugin()
			f := map[string]string{"tls": mode, "tls_cert": cert, "tls_key": key}
			if mode == "true" {
				f["tls_ca"] = p.caPath
			}
			mustValue(t, collect(t, pl, fieldsFor(s, f)), "redis.up", 1)
		})
	}
	mu.Lock()
	if strings.Join(seen, ",") != "monitor,monitor" {
		t.Errorf("server saw client certificates %q", seen)
	}
	mu.Unlock()
	t.Run("a server that requires one, without one", func(t *testing.T) {
		s := serve(t, cfg, bulk(primaryInfo()))
		pl, _ := newTestPlugin()
		mustDown(t, collect(t, pl, fieldsFor(s, map[string]string{"tls": "true", "tls_ca": p.caPath})), "TLS handshake failed")
	})
	t.Run("a mismatched key is named before anything dials", func(t *testing.T) {
		_, otherKey := p.client(t, "other")
		pl, _ := newTestPlugin()
		obs := collect(t, pl, map[string]string{"tls": "true", "tls_cert": cert, "tls_key": otherKey})
		mustErr(t, obs, "redis.up", "redis: secret field `tls_key` does not match `tls_cert`")
	})
}
