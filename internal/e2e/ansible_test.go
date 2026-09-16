// The milestone gate (backlog 2.12a): one scenario that exercises the
// whole v0.2b backend the way an operator drives it — over the real HTTP
// API of a fully wired manager (internal/app.Build: reaper, retest
// scheduler, CA, worker gRPC endpoint) with the worker's claim loop running
// in-process over loopback mTLS, against a TimescaleDB testcontainer, an
// exec-only SSH host behind a jump bastion and an SSH git server.
//
// Runner modes: fake (default — the Task 9 fixture pattern, fast and
// deterministic) and real (PULSIX_E2E_REAL_ANSIBLE=1 — the real
// ansible-runner drives ansible-playbook against the fixtures; skipped
// when ansible-runner is absent, e.g. a plain laptop without it — CI
// installs ansible-runner 2.4.3 + ansible-core 2.21.4 and runs both).
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

// The SSH fixtures bind dynamic ports; the golden fixture normalizes them
// (see NormalizeInventory) and the scenario asserts the live values
// separately.

// greetTemplate is the editor playbook the run template uses (the fixture
// file's content — the same document the fixture git repo carries).
func greetTemplate(t *testing.T) string { return fixtureFile(t, "playbooks/hello.yml") }

func failTemplate(t *testing.T) string { return fixtureFile(t, "playbooks/fail.yml") }

func fixtureFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// scenario is one wired-up gate run: the fixtures, the manager app, the
// worker loop and the API session, plus the ids each step hands the next.
type scenario struct {
	t   *testing.T
	api *apiClient
	org string

	hostLoginCred string // credential ids
	bastionCred   string
	deployCred    string

	hostLoginPub ssh.PublicKey // the credentials' public keys
	bastionPub   ssh.PublicKey
	deployPub    ssh.PublicKey

	bastionID string
	hostID    string
	zoneID    string

	repoID   string
	groupID  string
	helloPB  string
	failPB   string
	helloTID string
	failTID  string

	host     *hostSSHServer
	bastionS *bastionServer
	greeting string
}

func TestAnsibleEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := wireScenario(t, ctx, cancel)
	s.drive()
}

// resolveRunner picks the runner mode: the real ansible-runner when the
// env asks for it (and it is installed), the deterministic fake otherwise.
func resolveRunner(t *testing.T) (runnerBin, ansibleBin string) {
	t.Helper()
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") != "1" {
		return writeFakeRunner(t), ""
	}
	bin, err := exec.LookPath("ansible-runner")
	if err != nil {
		t.Skipf("PULSIX_E2E_REAL_ANSIBLE=1 but ansible-runner is not installed: %v", err)
	}
	ansible, err := exec.LookPath("ansible-playbook")
	if err != nil {
		t.Skipf("PULSIX_E2E_REAL_ANSIBLE=1 but ansible-playbook is not installed: %v", err)
	}
	return bin, ansible
}

// portOf returns the port part of a host:port address.
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// wireScenario builds everything the drive steps use: the fully wired
// manager, the enrolled worker's claim loop, the authenticated API session
// and the SSH fixtures (their authorized keys are the ones the generated
// credentials actually carry).
func wireScenario(t *testing.T, ctx context.Context, cancel context.CancelFunc) *scenario {
	t.Helper()

	// --- The manager: full production wiring ----------------------------
	httpLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	masterKey := [32]byte{'e', '2', 'e', '-', 'm', 'a', 's', 't', 'e', 'r', '-', 'k', 'e', 'y', '0', '1'}
	cfg := config.Config{
		Listen:         httpLis.Addr().String(),
		DatabaseURL:    testdb.URL(t),
		PublicURL:      "http://localhost:" + portOf(httpLis.Addr().String()),
		MasterKey:      masterKey,
		GRPCListen:     "localhost:0", // the CA's SAN; the harness binds its own listener
		RetestInterval: 24 * time.Hour,
		Version:        "e2e",
	}
	a, err := app.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	// The worker gRPC endpoint: app.Build built it because GRPCListen was
	// set; the harness serves it on its own loopback listener — the same
	// thing cmd/manager does with PULSIX_GRPC_LISTEN. The TLS ServerName
	// the worker dials with is the listen host ("localhost"), which is
	// exactly the server certificate's DNS SAN (Task 8's caveat).
	grpcLis, err := net.Listen("tcp", "localhost:0")
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

	httpSrv := &http.Server{Handler: a.Handler}
	go func() { _ = httpSrv.Serve(httpLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	base := "http://localhost:" + portOf(httpLis.Addr().String())

	// --- Operator session: setup wizard, mandatory TOTP enrolment --------
	org := "e2e-" + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String()[:8], "-", "")
	api := newAPIClient(t, base)
	api.do(http.MethodPost, "/api/v1/setup/complete", map[string]any{
		"admin_email":    "root@e2e.test",
		"admin_password": "correct horse battery",
		"admin_name":     "E2E Admin",
		"org_slug":       org,
		"org_name":       "E2E Org",
	}, nil)
	// A second factor is mandatory: the completed wizard's session is the
	// enrolment session, and confirming TOTP completes it in place.
	var enrolOut struct {
		Secret string `json:"secret"`
	}
	api.do(http.MethodPost, "/api/v1/auth/totp/enrol", struct{}{}, &enrolOut)
	code, err := totp.GenerateCode(enrolOut.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	api.do(http.MethodPost, "/api/v1/auth/totp/confirm", map[string]string{"code": code}, nil)

	// The zone the worker claims for: the organization's default zone.
	var zonesOut struct {
		Zones []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"zones"`
	}
	api.do(http.MethodGet, "/api/v1/orgs/"+org+"/zones", nil, &zonesOut)
	zoneID := uuid.Nil
	for _, z := range zonesOut.Zones {
		if z.Slug == "default" {
			zoneID = uuid.MustParse(z.ID)
		}
	}
	if zoneID == uuid.Nil {
		t.Fatal("the organization carries no default zone after setup")
	}

	// --- Worker enrollment: the operator CLI path -------------------------
	// `pulsix-manager worker add` against the same database and master key:
	// it issues the client certificate from the instance CA (the same one
	// Build's gRPC server verifies against) and registers the serial — the
	// exact output the worker binary is configured from.
	enrollCfg := config.Config{DatabaseURL: testdb.URL(t), MasterKey: masterKey, GRPCListen: "localhost:0"}
	var enroll strings.Builder
	if err := app.RunWorkerAdd(ctx, enrollCfg, app.WorkerAddOptions{ZoneSlug: "default", OrgSlug: org, Name: "e2e-worker"}, &enroll, io.Discard); err != nil {
		t.Fatalf("worker add: %v", err)
	}
	certPEM, keyPEM, caPEM, err := ParseEnrollmentBlocks(enroll.String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(dir, "worker.crt"): certPEM,
		filepath.Join(dir, "worker.key"): keyPEM,
		filepath.Join(dir, "ca.pem"):     caPEM,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := &scenario{t: t, api: api, org: org, zoneID: zoneID.String(), greeting: "hello e2e"}

	// --- The three generated credentials, over the API --------------------
	s.setupCredentials()

	// --- The SSH fixtures, authorizing exactly those keys ------------------
	// The host server prefers 127.0.0.2 (bindable on the Linux CI runner,
	// not on macOS); the bastion and the git server always bind 127.0.0.1.
	bindHost := loopbackHost(t)
	s.host = startHostSSHServer(t, s.hostLoginPub, bindHost)
	s.bastionS = startBastion(t, s.bastionPub)
	gitSrv := startGitServer(t, s.deployPub)
	s.setupHostAndBastion()

	// --- The worker's claim loop, in-process ------------------------------
	runnerBin, ansibleBin := resolveRunner(t)
	workerCfg := worker.Config{
		ManagerAddr:      "localhost:" + portOf(grpcLis.Addr().String()),
		CertPath:         filepath.Join(dir, "worker.crt"),
		KeyPath:          filepath.Join(dir, "worker.key"),
		CAPath:           filepath.Join(dir, "ca.pem"),
		DataDir:          t.TempDir(),
		Zones:            []uuid.UUID{zoneID},
		AnsibleRunnerBin: runnerBin,
		AnsibleBin:       ansibleBin,
		ClaimInterval:    200 * time.Millisecond,
		Version:          "e2e",
	}
	conn, err := worker.Dial(ctx, workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	runExec := runner.NewRunner(runnerBin, ansibleBin, "git", 0, testLogger(t))
	loop := worker.NewLoop(workerCfg, workerpb.NewWorkerServiceClient(conn),
		worker.NewCompositeExecutor(map[string]worker.Executor{
			"run":             runExec,
			"adhoc":           runExec,
			"bootstrap":       worker.NewBootstrapExecutor(runExec),
			"rotate_key":      worker.NewRotateExecutor(runExec),
			"revoke_key":      worker.NewRevokeExecutor(runExec),
			"connection_test": worker.NewSSHProbeExecutor(&sshprobe.Probe{}, testLogger(t)),
			"sync_repo":       worker.NewSyncExecutor(runExec, "ansible-galaxy"),
		}),
		worker.WithLogger(testLogger(t)),
	)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = loop.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel() // the loop's clean shutdown: cancellation, not connection loss
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Log("worker loop did not stop in time")
		}
	})

	// --- The remaining registry objects, over the API ---------------------
	s.setupRepo(gitSrv)
	s.setupInventory()
	return s
}

// testLogger keeps the worker's structured logs quiet unless the test
// verbosely runs.
func testLogger(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
