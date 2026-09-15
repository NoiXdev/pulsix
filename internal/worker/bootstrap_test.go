// The bootstrap executor's tests: the pin preflight and the claim
// reshaping (asserted through a capture delegate). The full delegation
// into the real ansible-runner machinery runs in system_e2e_test.go and
// Task 14's E2E.
package worker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net"
	"strconv"
	"strings"
	"testing"

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
)

// secretStr encodes a Secret as the claim's Secret JSON.
func secretStr(t *testing.T, s credentials.Secret) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// testClientPEM generates one ed25519 private key's PEM for the key-login
// fixtures.
func testClientPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

// bootstrapClaim is the claim payload shape internal/jobs ships.
func bootstrapClaim(t *testing.T, host map[string]any, loginUser, secretJSON, publicKey, pulsixUser string) string {
	t.Helper()
	return sysPayload(t, map[string]any{
		"host": host, "login_user": loginUser, "login": secretJSON,
		"public_key": publicKey, "pulsix_user": pulsixUser,
	})
}

// executorServer is the pin preflight's in-process SSH target.
type pinServer struct {
	addr        string
	fingerprint string
}

// startExecutorServer starts a gliderlabs SSH server with one host key.
func startPinServer(t *testing.T) *pinServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &glssh.Server{
		Handler: func(glssh.Session) {},
		PublicKeyHandler: func(glssh.Context, glssh.PublicKey) bool {
			return false // nothing is ever authenticated: the exchange is the test
		},
	}
	srv.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &pinServer{addr: ln.Addr().String(), fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
}

// hostAddress splits the fixture's listen address.
func (s *pinServer) hostAddress() (string, int) {
	host, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(err)
	}
	return host, port
}

func bootstrapExec(delegate Executor) *BootstrapExecutor {
	return NewBootstrapExecutor(delegate)
}

func TestBootstrapExecutorReshapesTheClaimForTheRunner(t *testing.T) {
	cap := &captureExecutor{status: StatusSucceeded, stats: map[string]any{}}
	host := map[string]any{"id": "host-uuid", "name": "web1", "address": "192.0.2.10", "port": 22}
	status, stats, err := bootstrapExec(cap).Run(context.Background(),
		sysJob(t, "bootstrap", bootstrapClaim(t, host, "root", secretStr(t, credentials.Secret{Password: "one-time-pw"}), "ssh-ed25519 AAAA pubkey", "ansible"), nil),
		RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}

	// The synthetic claim keeps the job's identity and kind (the bookends
	// and the playbook materialization stay truthful to what ran).
	got := cap.got
	if got.GetId() != "01900000-0000-7000-8000-000000000abc" || got.GetKind() != "bootstrap" {
		t.Fatalf("identity = %+v", got)
	}

	// The one-host inventory carries the connection values; the one-time
	// login rides in the credentials map and the runner's merge folds it
	// into the hostvars.
	var doc struct {
		All  map[string]any `json:"all"`
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(got.GetInventoryDoc()), &doc); err != nil {
		t.Fatalf("inventory doc: %v\n%s", err, got.GetInventoryDoc())
	}
	vars := doc.Meta.Hostvars["web1"]
	if vars["ansible_host"] != "192.0.2.10" || vars["ansible_user"] != "root" {
		t.Fatalf("hostvars = %v", vars)
	}

	// The connections map the runner's stats and key files ride on.
	var conn map[string]any
	if err := json.Unmarshal([]byte(got.GetConnections()["web1"]), &conn); err != nil {
		t.Fatalf("connection: %v", err)
	}
	if conn["host_id"] != "host-uuid" || conn["credential_id"] != "pulsix-login" {
		t.Fatalf("connection = %v", conn)
	}
	if creds := got.GetCredentials()["pulsix-login"]; creds != `{"password":"one-time-pw"}` {
		t.Fatalf("login credential = %q", creds)
	}

	// The public key and the account travel as extra vars, JSON-encoded.
	if got.GetExtraVars()["pulsix_public_key"] != `"ssh-ed25519 AAAA pubkey"` || got.GetExtraVars()["pulsix_user"] != `"ansible"` {
		t.Fatalf("extra vars = %v", got.GetExtraVars())
	}
	if got.GetSshUsername() != "root" {
		t.Fatalf("ssh username = %q", got.GetSshUsername())
	}
	if stats == nil {
		t.Error("the delegate's stats are passed through")
	}
}

func TestBootstrapExecutorPrivateKeyLoginRidesAsItsKeyFile(t *testing.T) {
	cap := &captureExecutor{status: StatusSucceeded}
	pem := testClientPEM(t)
	host := map[string]any{"id": "u1", "name": "web1", "address": "192.0.2.10", "port": 22}
	if _, _, err := bootstrapExec(cap).Run(context.Background(),
		sysJob(t, "bootstrap", bootstrapClaim(t, host, "root", secretStr(t, credentials.Secret{PrivateKey: pem}), "pub", "ansible"), nil),
		RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if creds := cap.got.GetCredentials()["pulsix-login"]; !strings.Contains(creds, "PRIVATE KEY") {
		t.Fatalf("login credential = %q, want the key material (the runner writes it to its 0600 key file)", creds)
	}
}

func TestBootstrapExecutorPinMismatchFailsClosed(t *testing.T) {
	srv := startPinServer(t)
	hostName, port := srv.hostAddress()
	pinned := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	cap := &captureExecutor{}
	job := sysJob(t, "bootstrap", bootstrapClaim(t, map[string]any{
		"id": "u1", "name": "web1", "address": hostName, "port": port,
	}, "root", secretStr(t, credentials.Secret{Password: "pw"}), "pub", "ansible"),
		map[string]string{"web1": pinned})
	status, _, err := bootstrapExec(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
	if !strings.Contains(err.Error(), "host key changed") {
		t.Errorf("error = %v, want it to name the host key change", err)
	}
	if cap.got != nil {
		t.Error("the runner must never run on a changed host key")
	}
}

func TestBootstrapExecutorPinnedHostWithMatchingKeyProceeds(t *testing.T) {
	srv := startPinServer(t)
	hostName, port := srv.hostAddress()

	cap := &captureExecutor{status: StatusSucceeded}
	job := sysJob(t, "bootstrap", bootstrapClaim(t, map[string]any{
		"id": "u1", "name": "web1", "address": hostName, "port": port,
	}, "root", secretStr(t, credentials.Secret{Password: "pw"}), "pub", "ansible"),
		map[string]string{"web1": srv.fingerprint})
	status, _, err := bootstrapExec(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("pinned match = %q, %v", status, err)
	}
	if cap.got == nil {
		t.Fatal("a matching pin lets the run proceed")
	}
}

func TestBootstrapExecutorUnreachablePinnedHostFailsClosed(t *testing.T) {
	cap := &captureExecutor{}
	job := sysJob(t, "bootstrap", bootstrapClaim(t, map[string]any{
		"id": "u1", "name": "web1", "address": "192.0.2.9", "port": 22,
	}, "root", secretStr(t, credentials.Secret{Password: "pw"}), "pub", "ansible"),
		map[string]string{"web1": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	_, _, err := bootstrapExec(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err == nil {
		t.Fatal("a pin that cannot be verified must fail the job closed")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Errorf("error names the cause: %v", err)
	}
	if cap.got != nil {
		t.Error("the runner must never run without a verified pin")
	}
}

func TestBootstrapExecutorUnpinnedHostPreflightsNothing(t *testing.T) {
	// An unroutable address with no pin: the executor must not dial at all
	// (TOFU); the run proceeds with the delegate.
	cap := &captureExecutor{status: StatusSucceeded}
	job := sysJob(t, "bootstrap", bootstrapClaim(t, map[string]any{
		"id": "u1", "name": "web1", "address": "192.0.2.9", "port": 22,
	}, "root", secretStr(t, credentials.Secret{Password: "pw"}), "pub", "ansible"), nil)
	status, _, err := bootstrapExec(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err != nil || status != StatusSucceeded || cap.got == nil {
		t.Fatalf("unpinned bootstrap = %q, %v, %v (no preflight dial may happen)", status, err, cap.got)
	}
}

func TestBootstrapExecutorBrokenPayloadsFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"not json", `{oops`, "kind payload"},
		{"no host", `{"login":"{}"}`, "no target"},
		{"no login", `{"host":{"name":"h","address":"a"}}`, "no target"},
		{"no public key", `{"host":{"name":"h","address":"a"},"login":"{}"}`, "no target"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _, err := bootstrapExec(&captureExecutor{}).Run(context.Background(),
				sysJob(t, "bootstrap", c.payload, nil), RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
			if err == nil || status != StatusError {
				t.Fatalf("status = %q err = %v", status, err)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %v, want it to name %q", err, c.wantErr)
			}
		})
	}
}

func TestBootstrapExecutorUnknownKindIsAnError(t *testing.T) {
	status, _, err := bootstrapExec(&captureExecutor{}).Run(context.Background(),
		sysJob(t, "run", `{}`, nil), RunEnv{}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
	if !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("error names the kind: %v", err)
	}
}
