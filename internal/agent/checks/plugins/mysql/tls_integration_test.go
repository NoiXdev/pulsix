package mysql

import (
	"bytes"
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
	"github.com/noixdev/pulsix/internal/containertest"
	"github.com/testcontainers/testcontainers-go"
)

// TestAgainstARealServerWithAClientCertificate runs the plugin against
// MySQL 8.4 with the test CA's server certificate and a user that must
// present a client certificate (REQUIRE X509; backlog 3.10a).
func TestAgainstARealServerWithAClientCertificate(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	ca := testpki.NewCA(t)
	srv := ca.Server(t, "localhost", "127.0.0.1", "::1")
	file := func(name string, data []byte) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{Reader: bytes.NewReader(data), ContainerFilePath: "/tls/" + name, FileMode: 0o644}
	}
	host, port := startServer(t, flavors[0], func(r *testcontainers.ContainerRequest) {
		r.Files = []testcontainers.ContainerFile{file("ca.pem", ca.PEM), file("server.pem", srv.CertPEM), file("server.key", srv.KeyPEM)}
		r.Cmd = []string{"--ssl-ca=/tls/ca.pem", "--ssl-cert=/tls/server.pem", "--ssl-key=/tls/server.key"}
	})
	ctx := context.Background()
	admin, err := openAdmin(host, port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	for _, q := range []string{
		`CREATE USER 'certuser'@'%' IDENTIFIED BY 'cert-pw' REQUIRE X509`,
		`GRANT REPLICATION CLIENT ON *.* TO 'certuser'@'%'`,
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	dir := t.TempDir()
	client := ca.Client(t, "certuser")
	caPath := testpki.Write(t, dir, "ca.pem", ca.PEM, 0o644)
	certPath := testpki.Write(t, dir, "client.pem", client.CertPEM, 0o644)
	keyPath := testpki.Write(t, dir, "client.key", client.KeyPEM, 0o600)
	fields := func(extra map[string]string) map[string]string {
		f := map[string]string{"host": host, "port": port, "user": "certuser", "password": "cert-pw", "tls": "true", "tls_ca": caPath}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	collectReal := func(t *testing.T, f map[string]string) string {
		t.Helper()
		obs, err := New().(*plugin).CollectWithSecret(ctx, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		up, _ := find(obs, "", "mysql.up")
		if up.Value != nil && *up.Value == 1 {
			return ""
		}
		o, _ := find(obs, "", "mysql.connections")
		return o.Err
	}

	t.Run("verified server and client certificate", func(t *testing.T) {
		if got := collectReal(t, fields(map[string]string{"tls_cert": certPath, "tls_key": keyPath})); got != "" {
			t.Fatalf("down: %q", got)
		}
	})
	t.Run("a client certificate with preferred", func(t *testing.T) {
		f := fields(map[string]string{"tls": "preferred", "tls_cert": certPath, "tls_key": keyPath})
		delete(f, "tls_ca")
		if got := collectReal(t, f); got != "" {
			t.Fatalf("down: %q", got)
		}
	})
	t.Run("no client certificate", func(t *testing.T) {
		if got := collectReal(t, fields(nil)); got != "mysql: authentication failed (error 1045)" {
			t.Fatalf("error = %q", got)
		}
	})
	t.Run("a server certificate of another CA", func(t *testing.T) {
		other := testpki.Write(t, dir, "other-ca.pem", testpki.NewCA(t).PEM, 0o644)
		if got := collectReal(t, fields(map[string]string{"tls_ca": other, "tls_cert": certPath, "tls_key": keyPath})); got != "mysql: TLS handshake failed" {
			t.Fatalf("error = %q", got)
		}
	})
	t.Run("a client certificate of another CA", func(t *testing.T) {
		o := testpki.NewCA(t).Client(t, "certuser")
		f := fields(map[string]string{
			"tls_cert": testpki.Write(t, dir, "other.pem", o.CertPEM, 0o644),
			"tls_key":  testpki.Write(t, dir, "other.key", o.KeyPEM, 0o600),
		})
		// The server's alert arrives after the client's TLS 1.3 handshake
		// finished; the driver turns the failed read into its own invalid
		// connection error, so the TLS cause is lost (postgres and redis
		// report "TLS handshake failed" here).
		if got := collectReal(t, f); got != "mysql: connection failed" {
			t.Fatalf("error = %q", got)
		}
	})
}
