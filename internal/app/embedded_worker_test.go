// The embedded worker's wiring integration test (Task 17's own brief
// item): Build with PULSIX_EMBEDDED_WORKER wires an in-process worker
// that claims and completes a trivial job over real loopback mTLS,
// against the shared testcontainer database.
//
// cfg.GRPCListen is deliberately host-less (":<port>", never
// "127.0.0.1:<port>"): the server certificate's DNS SAN then comes
// entirely from config.Config.CAListenHost's fallback default, and the
// embedded worker's loopback dial only succeeds because internal/app
// passes that same name as the worker's PULSIX_MANAGER_SERVER_NAME
// override. That is exactly the ledger's forwarded gap ("the worker's
// mTLS client has no ServerName override … a listener address like
// ':8444' has no usable host") — this test only turns green once both the
// config default and the worker override are wired correctly.
//
// The trivial job is a connection_test: it needs no ansible-runner or git
// (unlike run/adhoc/sync_repo), just a real SSH target — an in-process
// gliderlabs/ssh fixture, the same test-only dependency
// internal/worker/sshprobe's own tests use — so the whole scenario stays
// hermetic and fast.
package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"testing"
	"time"

	glssh "github.com/gliderlabs/ssh"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/ssh"
)

// freePort picks an ephemeral TCP port on loopback and releases it
// immediately — the small reuse race is the same one internal/e2e accepts
// for its own fixed compose ports; CI runners are otherwise idle here.
func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	return port
}

// embeddedTestClient is a minimal cookie-jar JSON client — the same shape
// internal/e2e's own apiClient uses, trimmed to what this one scenario
// needs.
type embeddedTestClient struct {
	t    *testing.T
	base string
	http *http.Client
}

func newEmbeddedTestClient(t *testing.T, base string) *embeddedTestClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &embeddedTestClient{t: t, base: base, http: &http.Client{Jar: jar}}
}

// do performs one JSON round trip and asserts a 2xx status.
func (c *embeddedTestClient) do(method, path string, body, out any) {
	c.t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode %s %s body: %v", method, path, err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reqBody)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("%s %s: status %d, body %s", method, path, resp.StatusCode, raw)
	}
	if out == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		c.t.Fatalf("%s %s: decode response: %v", method, path, err)
	}
}

// startEmbeddedProbeServer runs a trivial gliderlabs/ssh target: it
// authenticates exactly the given public key, and every command (`sudo -n
// true`, `python3 -c 'print(1)'`) "succeeds" — the default session
// handler does nothing and gliderlabs exits 0 when a handler returns
// without calling Exit. It mirrors internal/worker/sshprobe's own test
// fixture (probe_test.go's startProbeServer), trimmed to the one
// all-checks-pass path this scenario needs.
func startEmbeddedProbeServer(t *testing.T, authorized ssh.PublicKey) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &glssh.Server{
		Handler: func(glssh.Session) {},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			return bytes.Equal(key.Marshal(), authorized.Marshal())
		},
	}
	srv.AddHostKey(hostSigner)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })
	return lis.Addr().String()
}

// TestEmbeddedWorkerClaimsAndCompletesConnectionTest is Task 17's required
// wiring test: the full manager (Build with PULSIX_EMBEDDED_WORKER, its
// gRPC endpoint actually bound and served — Build itself only constructs
// the grpc.Server, exactly like cmd/manager's own serve() does the
// bind/Serve afterwards), an operator session through the setup wizard, a
// generated SSH credential, a host pointed at an in-process SSH fixture,
// and a connection-test job the embedded worker must claim and complete
// entirely on its own — nothing in this test drives a worker directly.
func TestEmbeddedWorkerClaimsAndCompletesConnectionTest(t *testing.T) {
	// The embedded worker's data dir defaults to /var/lib/pulsix-worker —
	// fine inside the manager's container image (deploy/docker/Dockerfile.manager
	// creates it), not writable by an unprivileged test process; point it
	// at a scratch dir the same way an operator would with
	// PULSIX_WORKER_DATA_DIR.
	t.Setenv("PULSIX_WORKER_DATA_DIR", t.TempDir())

	ctx := context.Background()
	cfg := testConfig(t)
	port := freePort(t)
	cfg.GRPCListen = fmt.Sprintf(":%d", port) // no host: exercises CAListenHost's fallback default
	cfg.EmbeddedWorker = true

	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build with PULSIX_EMBEDDED_WORKER: %v", err)
	}
	defer a.Close()
	if a.GRPC == nil {
		t.Fatal("Build with GRPCListen set must produce a gRPC server")
	}

	grpcLis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.GRPC.Serve(grpcLis) }()
	t.Cleanup(func() {
		stopped := make(chan struct{})
		go func() { a.GRPC.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			a.GRPC.Stop()
		}
	})

	httpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := &http.Server{Handler: a.Handler}
	go func() { _ = httpSrv.Serve(httpLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	base := "http://" + httpLis.Addr().String()

	client := newEmbeddedTestClient(t, base)
	const org = "embedded-org"
	client.do(http.MethodPost, "/api/v1/setup/complete", map[string]any{
		"admin_email": "root@embedded.test", "admin_password": "correct horse battery",
		"admin_name": "Embedded Admin", "org_slug": org, "org_name": "Embedded Org",
	}, nil)
	// A second factor is mandatory before the session can drive any other
	// route (internal/e2e's own scenario does the same).
	var enrol struct {
		Secret string `json:"secret"`
	}
	client.do(http.MethodPost, "/api/v1/auth/totp/enrol", struct{}{}, &enrol)
	code, err := totp.GenerateCode(enrol.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client.do(http.MethodPost, "/api/v1/auth/totp/confirm", map[string]string{"code": code}, nil)

	prefix := "/api/v1/orgs/" + org
	var zonesOut struct {
		Zones []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"zones"`
	}
	client.do(http.MethodGet, prefix+"/zones", nil, &zonesOut)
	zoneID := ""
	for _, z := range zonesOut.Zones {
		if z.Slug == "default" {
			zoneID = z.ID
		}
	}
	if zoneID == "" {
		t.Fatal("organization carries no default zone after setup — the embedded worker has nothing to resolve")
	}

	var credOut struct {
		Credential struct {
			ID        string `json:"id"`
			PublicKey string `json:"public_key"`
		} `json:"credential"`
	}
	client.do(http.MethodPost, prefix+"/credentials", map[string]any{
		"name": "host-login", "kind": "ssh_key", "generate": true,
	}, &credOut)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(credOut.Credential.PublicKey))
	if err != nil {
		t.Fatalf("parse generated public key: %v", err)
	}

	targetAddr := startEmbeddedProbeServer(t, pub)
	targetHost, targetPort, err := net.SplitHostPort(targetAddr)
	if err != nil {
		t.Fatal(err)
	}
	portNum, err := strconv.Atoi(targetPort)
	if err != nil {
		t.Fatal(err)
	}

	var hostOut struct {
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
	}
	client.do(http.MethodPost, prefix+"/hosts", map[string]any{
		"name": "embedded-e2e-host", "zone_id": zoneID, "address": targetHost, "managed": true,
	}, &hostOut)
	hostID := hostOut.Host.ID

	client.do(http.MethodPut, prefix+"/hosts/"+hostID+"/ssh", map[string]any{
		"username": "ansible", "port": portNum, "credential_id": credOut.Credential.ID,
	}, nil)

	var testOut struct {
		JobID string `json:"job_id"`
	}
	client.do(http.MethodPost, prefix+"/hosts/"+hostID+"/connection-test", struct{}{}, &testOut)
	if testOut.JobID == "" {
		t.Fatal("connection-test did not return a job id")
	}

	deadline := time.Now().Add(60 * time.Second)
	var status, jobErr string
	for {
		var jobOut struct {
			Job struct {
				Status string `json:"status"`
				Error  string `json:"error,omitempty"`
			} `json:"job"`
		}
		client.do(http.MethodGet, prefix+"/jobs/"+testOut.JobID, nil, &jobOut)
		status, jobErr = jobOut.Job.Status, jobOut.Job.Error
		if status == "succeeded" || status == "failed" || status == "cancelled" || status == "error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection test job did not finish in 60s; last status %q", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "succeeded" {
		t.Fatalf("connection test job = %q (error %q) — the embedded worker did not claim and complete it", status, jobErr)
	}
}
