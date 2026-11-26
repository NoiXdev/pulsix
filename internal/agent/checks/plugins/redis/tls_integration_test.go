package redis

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
	"github.com/noixdev/pulsix/internal/containertest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestAgainstARealServerWithAClientCertificate runs the plugin against a
// Redis that only listens on TLS and requires a client certificate signed
// by the test CA (backlog 3.10a).
func TestAgainstARealServerWithAClientCertificate(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	ctx := context.Background()
	ca := testpki.NewCA(t)
	srv := ca.Server(t, "localhost", "127.0.0.1", "::1")
	file := func(name string, data []byte) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{Reader: bytes.NewReader(data), ContainerFilePath: "/tls/" + name, FileMode: 0o644}
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7.4-alpine",
			ExposedPorts: []string{"6379/tcp"},
			Files:        []testcontainers.ContainerFile{file("ca.pem", ca.PEM), file("server.pem", srv.CertPEM), file("server.key", srv.KeyPEM)},
			Cmd: []string{
				"redis-server", "--port", "0", "--tls-port", "6379",
				"--tls-cert-file", "/tls/server.pem", "--tls-key-file", "/tls/server.key",
				"--tls-ca-cert-file", "/tls/ca.pem", "--tls-auth-clients", "yes",
				"--requirepass", serverPassword,
			},
			WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start redis:7.4-alpine: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := c.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	client := ca.Client(t, "monitor")
	caPath := testpki.Write(t, dir, "ca.pem", ca.PEM, 0o644)
	certPath := testpki.Write(t, dir, "client.pem", client.CertPEM, 0o644)
	keyPath := testpki.Write(t, dir, "client.key", client.KeyPEM, 0o600)
	fields := func(extra map[string]string) map[string]string {
		f := map[string]string{"host": host, "port": mapped.Port(), "password": serverPassword, "tls": "true", "tls_ca": caPath}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}

	t.Run("verified server and client certificate", func(t *testing.T) {
		obs, err := New().(*plugin).CollectWithSecret(ctx, nil, fields(map[string]string{"tls_cert": certPath, "tls_key": keyPath}))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "redis.up", 1)
		mustLabel(t, obs, "redis.role", "primary")
	})
	t.Run("no client certificate", func(t *testing.T) {
		obs, err := New().(*plugin).CollectWithSecret(ctx, nil, fields(nil))
		if err != nil {
			t.Fatal(err)
		}
		mustDown(t, obs, "TLS handshake failed")
	})
	t.Run("a server certificate of another CA", func(t *testing.T) {
		other := testpki.Write(t, dir, "other-ca.pem", testpki.NewCA(t).PEM, 0o644)
		obs, err := New().(*plugin).CollectWithSecret(ctx, nil, fields(map[string]string{"tls_ca": other, "tls_cert": certPath, "tls_key": keyPath}))
		if err != nil {
			t.Fatal(err)
		}
		mustDown(t, obs, "TLS handshake failed")
	})
}
