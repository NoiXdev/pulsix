// The milestone gate (backlog 2.12a): one scenario that exercises the
// whole v0.2b backend the way an operator drives it — over the real HTTP
// API of a fully wired manager (internal/app.Build: reaper, retest
// scheduler, CA, worker gRPC endpoint) with the worker's claim loop running
// in-process over loopback mTLS, against a TimescaleDB testcontainer, an
// exec-only SSH host behind a jump bastion and an SSH git server.
//
// Runner modes: fake (default — the Task 9 fixture pattern, fast and
// deterministic) and real (PULSIX_E2E_REAL_ANSIBLE=1 — the real
// ansible-runner drives ansible-playbook against the fixtures; the test
// FAILS when either binary is missing — CI installs ansible-runner 2.4.3 +
// ansible-core 2.21.4 and runs both).
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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
	"github.com/noixdev/pulsix/internal/setup"
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
	git      *gitServer // the fixture repository's server (variables_test.go pushes to it)
	greeting string

	runnerLog string // the capture wrapper's log (see captureRunnerOutput)

	workerCfg worker.Config // the claim loop's config (the automation gate dials with its identity)
}

func TestAnsibleEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := wireScenario(t, ctx, cancel, wiring{})
	s.drive()
}

// resolveRunner picks the runner mode: the real ansible-runner when the
// env asks for it (and it is installed), the deterministic fake otherwise.
func resolveRunner(t *testing.T) (runnerBin, ansibleBin string) {
	t.Helper()
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") != "1" {
		return writeFakeRunner(t), ""
	}
	return resolveRealRunner(t)
}

// resolveRealRunner locates the installed ansible-runner and
// ansible-playbook binaries.
func resolveRealRunner(t *testing.T) (runnerBin, ansibleBin string) {
	t.Helper()
	// The real mode is the milestone gate: asked for and not runnable is a
	// failure, never a silent skip that reads as a pass.
	bin, err := exec.LookPath("ansible-runner")
	if err != nil {
		t.Fatalf("PULSIX_E2E_REAL_ANSIBLE=1 but ansible-runner is not installed: %v", err)
	}
	ansible, err := exec.LookPath("ansible-playbook")
	if err != nil {
		t.Fatalf("PULSIX_E2E_REAL_ANSIBLE=1 but ansible-playbook is not installed: %v", err)
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

// wiring selects the variant a scenario is built with. The zero value is
// the v0.2b gate: a worker enrolled with the `pulsix-manager worker add`
// bundle and the production schedule tick.
type wiring struct {
	// tokenWorker enrolls the worker over the API with a one-time token
	// (v0.2c) instead of the CLI bundle.
	tokenWorker bool
	// scheduleTick overrides PULSIX_SCHEDULE_TICK (0 keeps the default).
	scheduleTick time.Duration
	// opts are app.Build's test seams (app.WithScheduleClock).
	opts []app.Option
	// notifyAllow sets PULSIX_NOTIFY_ALLOW_CIDRS (v0.2d): the notification
	// gate admits its loopback webhook receiver.
	notifyAllow []netip.Prefix
}

// wireScenario builds everything the drive steps use: the fully wired
// manager, the enrolled worker's claim loop, the authenticated API session
// and the SSH fixtures (their authorized keys are the ones the generated
// credentials actually carry).
func wireScenario(t *testing.T, ctx context.Context, cancel context.CancelFunc, w wiring) *scenario {
	t.Helper()

	// --- The manager: full production wiring ----------------------------
	httpLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	// The worker gRPC listener exists before Build so the enrollment
	// snippets and the token path name its real address.
	grpcLis, err := net.Listen("tcp", "localhost:0")
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
		GRPCPublicAddr: "localhost:" + portOf(grpcLis.Addr().String()),
		RetestInterval: 24 * time.Hour,
		ScheduleTick:   w.scheduleTick,
		// The revoke-time requeue (unlike the reaper) applies this as given,
		// with no <=0 fallback of its own — config.Load always supplies the
		// production default, so match it here rather than requeue on 0
		// attempts allowed.
		JobMaxAttempts:   config.DefaultJobMaxAttempts,
		Version:          "e2e",
		NotifyAllowCIDRs: w.notifyAllow,
	}
	a, err := app.Build(ctx, cfg, w.opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	// The worker gRPC endpoint: app.Build built it because GRPCListen was
	// set; the harness serves it on its own loopback listener — the same
	// thing cmd/manager does with PULSIX_GRPC_LISTEN. The TLS ServerName
	// the worker dials with is the listen host ("localhost"), which is
	// exactly the server certificate's DNS SAN (Task 8's caveat).
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
		"setup_token":    setup.Token(masterKey),
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

	s := &scenario{t: t, api: api, org: org, zoneID: zoneID.String(), greeting: "hello e2e"}
	managerAddr := "localhost:" + portOf(grpcLis.Addr().String())

	// --- Worker enrollment: the CLI bundle or a one-time token -------------
	var identity worker.Config
	if w.tokenWorker {
		identity = s.enrollWithToken(ctx, managerAddr)
	} else {
		identity = enrollWithCLI(t, ctx, masterKey, org)
	}

	// --- The three generated credentials, over the API --------------------
	s.setupCredentials()

	// --- The SSH fixtures, authorizing exactly those keys ------------------
	// The host server prefers 127.0.0.2 (bindable on the Linux CI runner,
	// not on macOS); the bastion and the git server always bind 127.0.0.1.
	bindHost := loopbackHost(t)
	s.host = startHostSSHServer(t, s.hostLoginPub, bindHost)
	s.bastionS = startBastion(t, s.bastionPub)
	gitSrv := startGitServer(t, s.deployPub)
	s.git = gitSrv
	s.setupHostAndBastion()

	// --- The worker's claim loop, in-process ------------------------------
	runnerBin, ansibleBin := resolveRunner(t)
	// The worker never puts runner output on the job (it can carry
	// secrets), so a run that errors reads only "exited with code N". The
	// capture wrapper keeps the runner's stdout and stderr in a test-owned
	// log that the run assertions print on failure (runnerOutput).
	runnerBin, s.runnerLog = captureRunnerOutput(t, runnerBin)
	workerCfg := worker.Config{
		ManagerAddr:      managerAddr,
		CertPath:         identity.CertPath,
		KeyPath:          identity.KeyPath,
		CAPath:           identity.CAPath,
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
	s.workerCfg = workerCfg
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

// enrollWithCLI is the v0.2b path: `pulsix-manager worker add` against the
// same database and master key issues the client certificate from the
// instance CA (the same one Build's gRPC server verifies against) and
// registers the serial — the exact output the worker binary is configured
// from.
func enrollWithCLI(t *testing.T, ctx context.Context, masterKey [32]byte, org string) worker.Config {
	t.Helper()
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
	cfg := worker.Config{
		CertPath: filepath.Join(dir, "worker.crt"),
		KeyPath:  filepath.Join(dir, "worker.key"),
		CAPath:   filepath.Join(dir, "ca.pem"),
	}
	for path, content := range map[string]string{cfg.CertPath: certPEM, cfg.KeyPath: keyPEM, cfg.CAPath: caPEM} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// testLogger keeps the worker's structured logs quiet unless the test
// verbosely runs.
func testLogger(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
