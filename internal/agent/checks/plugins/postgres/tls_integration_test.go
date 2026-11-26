package postgres

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
	"github.com/noixdev/pulsix/internal/containertest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pgHBA lets the user certuser in only with a client certificate signed by
// the test CA (cert maps its common name to the user); everyone else logs
// in with a password.
const pgHBA = `local all all trust
hostssl all certuser all cert
host all all all scram-sha-256
`

// TestAgainstARealServerWithAClientCertificate runs the plugin against a
// PostgreSQL that verifies a client certificate (backlog 3.10a), with the
// server's certificate verified against a CA file.
func TestAgainstARealServerWithAClientCertificate(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	ctx := context.Background()
	ca := testpki.NewCA(t)
	srv := ca.Server(t, "localhost", "127.0.0.1", "::1")
	file := func(name string, data []byte) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{Reader: bytes.NewReader(data), ContainerFilePath: "/tls-src/" + name, FileMode: 0o644}
	}
	// The server refuses a key another user can read, so the files are
	// copied and handed to postgres before the entrypoint starts it.
	script := "mkdir -p /tls && cp /tls-src/* /tls/ && chown postgres /tls/* && chmod 600 /tls/server.key && " +
		"exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tls/server.pem -c ssl_key_file=/tls/server.key " +
		"-c ssl_ca_file=/tls/ca.pem -c hba_file=/tls/pg_hba.conf"
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testImage,
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_PASSWORD": "admin-pw"},
			Files: []testcontainers.ContainerFile{
				file("ca.pem", ca.PEM), file("server.pem", srv.CertPEM), file("server.key", srv.KeyPEM), file("pg_hba.conf", []byte(pgHBA)),
			},
			Entrypoint: []string{"sh", "-c", script},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	port := mapped.Port()
	admin, err := pgx.Connect(ctx, fmt.Sprintf("postgres://postgres:admin-pw@%s:%s/postgres?sslmode=require", host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, q := range []string{`CREATE ROLE certuser LOGIN`, `GRANT pg_monitor TO certuser`} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	dir := t.TempDir()
	client := ca.Client(t, "certuser")
	caPath := testpki.Write(t, dir, "ca.pem", ca.PEM, 0o644)
	certPath := testpki.Write(t, dir, "client.pem", client.CertPEM, 0o644)
	keyPath := testpki.Write(t, dir, "client.key", client.KeyPEM, 0o600)
	fields := func(extra map[string]string) map[string]string {
		f := map[string]string{"host": host, "port": port, "user": "certuser", "sslmode": "verify-full", "sslrootcert": caPath}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	p := New().(plugin)
	collectReal := func(t *testing.T, f map[string]string) []checks.Observation {
		t.Helper()
		obs, err := p.CollectWithSecret(ctx, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		return obs
	}

	t.Run("verified server and client certificate", func(t *testing.T) {
		obs := collectReal(t, fields(map[string]string{"sslcert": certPath, "sslkey": keyPath}))
		mustValue(t, obs, "", "pg.up", 1)
		mustValue(t, obs, "", "pg.in_recovery", 0)
	})
	t.Run("no client certificate", func(t *testing.T) {
		obs := collectReal(t, fields(nil))
		mustValue(t, obs, "", "pg.up", 0)
		o, _ := find(obs, "", "pg.connections")
		if !strings.HasPrefix(o.Err, "postgres: authentication failed") {
			t.Fatalf("error = %q", o.Err)
		}
	})
	t.Run("a client certificate of another CA", func(t *testing.T) {
		other := testpki.NewCA(t).Client(t, "certuser")
		obs := collectReal(t, fields(map[string]string{
			"sslcert": testpki.Write(t, dir, "other.pem", other.CertPEM, 0o644),
			"sslkey":  testpki.Write(t, dir, "other.key", other.KeyPEM, 0o600),
		}))
		mustValue(t, obs, "", "pg.up", 0)
		mustErr(t, obs, "", "pg.connections", "postgres: TLS handshake failed")
	})
	t.Run("a server certificate of another CA", func(t *testing.T) {
		obs := collectReal(t, fields(map[string]string{
			"sslrootcert": testpki.Write(t, dir, "other-ca.pem", testpki.NewCA(t).PEM, 0o644),
			"sslcert":     certPath, "sslkey": keyPath,
		}))
		mustValue(t, obs, "", "pg.up", 0)
		mustErr(t, obs, "", "pg.connections", "postgres: TLS handshake failed")
	})
}
