// Command worker is the Pulsix zone worker: it authenticates to the
// manager with the client certificate `pulsix-manager worker add` issued
// for it, claims queued jobs of its zones and executes them, streaming
// every run's events back to the manager over gRPC.
//
// Configuration comes from the environment. The names are the same
// `pulsix-manager worker add` prints on enrollment — see
// internal/worker/config.go for the authoritative list and defaults:
//
//	PULSIX_MANAGER_ADDR        manager's gRPC endpoint, host:port (required)
//	PULSIX_MANAGER_SERVER_NAME overrides the TLS ServerName verified against the manager
//	PULSIX_WORKER_CERT         this worker's client certificate PEM path (all three, or none)
//	PULSIX_WORKER_KEY          this worker's private key PEM path (all three, or none)
//	PULSIX_CA_CERT             CA certificate PEM path, pins the manager (all three, or none)
//	PULSIX_ENROLL_TOKEN        one-time token to enroll with when no bundle exists yet
//	PULSIX_WORKER_IDENTITY_DIR where enrollment writes/renewal replaces the certificate, default <data dir>/identity
//	PULSIX_WORKER_DATA_DIR     scratch root, default /var/lib/pulsix-worker
//	PULSIX_WORKER_ZONES        comma-separated zone ids; empty = what the certificate says
//	PULSIX_ANSIBLE_RUNNER_BIN  default ansible-runner
//	PULSIX_ANSIBLE_BIN         default ansible-playbook
//	PULSIX_CLAIM_INTERVAL      default 3s
//	PULSIX_MAX_CONCURRENT      default 1 (v0.2b runs one job at a time)
//
// Exit codes: 2 on a configuration error (the operator must act), 0 on a
// clean shutdown (SIGTERM/SIGINT), 1 on any other failure.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

// version is the worker's build version, reported to the manager on every
// claim so an operator can tell exactly what is running. It stays "dev"
// for a local `go build`/`go run`; the worker Dockerfile overrides it with
// `-ldflags "-X main.version=..."`, the same mechanism cmd/manager uses.
var version = "dev"

// main only translates run's exit code; the indirection keeps run's
// deferred closes executing (os.Exit from inside a function with pending
// defers skips them).
func main() {
	os.Exit(run(os.Environ()))
}

// run wires the worker process: load the configuration, run under the
// signal context, map the outcome to the exit code.
func run(env []string) int {
	cfg, err := worker.Load(env)
	if err != nil {
		slog.Error("configuration error", "err", err)
		return 2
	}
	cfg.Version = version

	// Cancelled on SIGTERM (the container orchestrator's graceful stop) or
	// SIGINT (Ctrl-C during local development); the loop shuts down cleanly
	// on either.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	slog.Info("pulsix-worker starting", "manager", cfg.ManagerAddr, "data_dir", cfg.DataDir)
	// A valid bundle wins; otherwise enroll with PULSIX_ENROLL_TOKEN.
	if err := worker.EnsureIdentity(ctx, cfg, slog.Default()); err != nil {
		slog.Error("no worker identity", "err", err)
		return 2
	}
	id, err := worker.LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		slog.Error("load worker identity", "err", err)
		return 1
	}
	conn, err := worker.DialIdentity(ctx, cfg, id)
	if err != nil {
		slog.Error("dial manager failed", "addr", cfg.ManagerAddr, "err", err)
		return 1
	}
	defer func() { _ = conn.Close() }() // best-effort: the process is exiting anyway
	client := workerpb.NewWorkerServiceClient(conn)
	// Renewal at start and every 6 h, once a third of the validity is left.
	go worker.NewRenewer(client, id, nil, nil, slog.Default()).Run(ctx)

	// The kind dispatch: the ansible-runner executor serves run and ad-hoc
	// jobs and the Task 11 system playbooks (bootstrap and rotate_key —
	// the two executors reshape their one-host claim and delegate to the
	// same runner), the ssh-probe executor serves connection tests and the
	// repo-sync executor serves sync_repo. A kind with no executor in this
	// build reports an infrastructure error — the claim is at-least-once,
	// the manager requeues it.
	runExec := runner.NewRunner(cfg.AnsibleRunnerBin, cfg.AnsibleBin, "git", 0, slog.Default())
	probeExec := worker.NewSSHProbeExecutor(&sshprobe.Probe{}, slog.Default())
	loop := worker.NewLoop(
		cfg, client,
		worker.NewCompositeExecutor(map[string]worker.Executor{
			"run":             runExec,
			"adhoc":           runExec,
			"bootstrap":       worker.NewBootstrapExecutor(runExec),
			"rotate_key":      worker.NewRotateExecutor(runExec),
			"revoke_key":      worker.NewRevokeExecutor(runExec),
			"connection_test": probeExec,
			"sync_repo":       worker.NewSyncExecutor(runExec, "ansible-galaxy"),
		}),
		worker.WithLogger(slog.Default()),
	)
	return workerLoop(ctx, loop)
}

// workerLoop runs the claim loop and maps its outcome to the exit code.
func workerLoop(ctx context.Context, loop *worker.Loop) int {
	if err := loop.Run(ctx); err != nil {
		slog.Error("worker loop stopped", "err", err)
		return 1
	}
	return 0
}
