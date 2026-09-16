// The embedded worker (PULSIX_EMBEDDED_WORKER=1, off by default): the same
// worker package cmd/worker drives, started in-process and authenticated
// over loopback mTLS. It enrolls itself through the exact
// `pulsix-manager worker add` path (resolveZone, ca.CA.Issue,
// workers.Service.Register — all defined in workeradd.go, reused
// unchanged), so an embedded worker is indistinguishable from one an
// operator enrolled by hand: same workers table row, same certificate
// serial the gRPC interceptor checks on every call.
//
// Opt-in and only for trusted single-tenant setups (spec §2/§16): the
// playbooks an embedded worker runs execute inside the manager process's
// container, as the manager's user, next to its secrets — a `hosts:
// localhost` task can read the manager's environment (the master key and
// the schema-owner database URL). The default deployment therefore runs a
// separate worker container that never holds those secrets; Build logs a
// warning whenever this is turned on. The shipped manager image carries no
// Ansible runtime, so in that image the embedded worker can run connection
// tests only (pure Go); every other job kind fails.
//
// It targets the "default" zone every organization gets
// (hosts.DefaultZoneSlug) — the zone `worker add --zone default` would
// resolve to on a single-organization instance. A fresh instance has no
// organization at Build time (the setup wizard runs after the manager
// starts serving HTTP), so resolution retries on a timer instead of
// failing Build; an instance with more than one organization is ambiguous
// by the same rule `worker add` enforces (each organization has its own
// "default" zone), so the embedded worker logs once and keeps waiting
// rather than guessing which one to serve. It never serves more than that
// one zone of one organization.
//
// Known limits, accepted because it is opt-in: every manager start
// registers a new worker row named "embedded" (the old rows stay until an
// operator deletes them), and a failure after zone resolution (issuing,
// registering, dialing) stops the embedded worker until the next restart.
package app

import (
	"context"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
	"github.com/noixdev/pulsix/internal/workers"
)

// embeddedWorkerWarning is the startup warning Build logs whenever the
// embedded worker is enabled.
const embeddedWorkerWarning = "PULSIX_EMBEDDED_WORKER is enabled: playbooks run inside the manager process " +
	"next to its master key and database credentials — use it only for trusted single-tenant setups " +
	"and run a separate worker container everywhere else"

// warnEmbeddedWorker logs embeddedWorkerWarning.
func warnEmbeddedWorker(log *slog.Logger) {
	log.Warn(embeddedWorkerWarning)
}

// embeddedWorkerRetryInterval is how often the embedded worker retries
// resolving its zone while no organization (or an ambiguous multiple) has
// a "default" zone yet.
const embeddedWorkerRetryInterval = 5 * time.Second

// embeddedWorkerName is the fixed name the embedded worker registers
// under. The workers table has no uniqueness constraint on name, only on
// certificate serial, so a restarted manager registering a second row
// under the same name is expected, not a conflict — the embedded worker's
// identity IS the manager process's, and a restart is a new one.
const embeddedWorkerName = "embedded"

// startEmbeddedWorker launches the embedded worker's resolve-and-run
// goroutine and returns a stop function that cancels it and waits for the
// goroutine to actually exit — Close calls it exactly like it stops the
// reaper and the retest scheduler. managerAddr is the loopback address the
// worker dials (127.0.0.1:<gRPC port>); serverName is the TLS ServerName
// it verifies the manager's server certificate against — it must be
// exactly cfg.CAListenHost(), the same name the server certificate's SAN
// was issued with, or the loopback handshake fails (see
// internal/worker.Config.ServerName's own doc comment).
func startEmbeddedWorker(appDB *db.DB, caInst *ca.CA, managerAddr, serverName string, log *slog.Logger) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runEmbeddedWorker(ctx, appDB, caInst, managerAddr, serverName, log)
	}()
	return func() {
		cancel()
		<-done
	}
}

// runEmbeddedWorker resolves the default zone, retrying on
// embeddedWorkerRetryInterval until it succeeds or ctx is cancelled, then
// runs the claim loop until ctx is cancelled. It reuses genZoneSource and
// resolveZone from workeradd.go: the exact same cross-organization lookup
// `pulsix-manager worker add` performs, run here with a bare context (no
// db.WithOrgScope bound) so appDB's org_isolation_bypass policy applies —
// the same pattern hostsSvc.ReconcileDefaultZones uses in app.go's Build
// for the same reason (a management-plane read that must see every
// organization, not the caller's own).
func runEmbeddedWorker(ctx context.Context, appDB *db.DB, caInst *ca.CA, managerAddr, serverName string, log *slog.Logger) {
	src := genZoneSource{q: gen.New(appDB.Pool())}
	ticker := time.NewTicker(embeddedWorkerRetryInterval)
	defer ticker.Stop()
	warned := false
	for {
		zone, err := resolveZone(ctx, src, hosts.DefaultZoneSlug, "")
		if err == nil {
			runEmbeddedLoop(ctx, appDB, caInst, zone, managerAddr, serverName, log)
			return
		}
		if !warned {
			log.WarnContext(ctx, "embedded worker: default zone not resolvable yet, retrying",
				"interval", embeddedWorkerRetryInterval, "err", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runEmbeddedLoop enrolls the embedded worker for zone (issue, register)
// and runs its claim loop until ctx is cancelled. The certificate and CA
// PEM are written to a private temp directory (0700 dir via os.MkdirTemp,
// 0600 files) removed when this function returns — the embedded worker's
// key material never touches the manager's own data directory.
//
// The executor set mirrors cmd/worker/main.go exactly: the ansible-runner
// executor for run/adhoc/bootstrap/rotate_key, the Go-native SSH prober
// for connection_test, and the repo-sync executor for sync_repo. A future
// change to that dispatch table must be made in both places.
func runEmbeddedLoop(ctx context.Context, appDB *db.DB, caInst *ca.CA, zone zoneRow, managerAddr, serverName string, log *slog.Logger) {
	certPEM, keyPEM, serial, err := caInst.Issue(ctx, &zone.OrgID, zone.ID, embeddedWorkerName)
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: issue certificate", "err", err)
		return
	}
	if _, err := workers.NewService(workers.NewSQLStore(appDB)).Register(ctx, zone.OrgID, workers.RegisterInput{
		ZoneID: zone.ID, Name: embeddedWorkerName, CertSerial: serial,
	}); err != nil {
		log.ErrorContext(ctx, "embedded worker: register", "err", err)
		return
	}

	dir, err := os.MkdirTemp("", "pulsix-embedded-worker-*")
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: create scratch directory", "err", err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	_, caCert := caInst.Pool()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	certPath, keyPath, caPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), filepath.Join(dir, "ca.pem")
	for path, content := range map[string][]byte{certPath: certPEM, keyPath: keyPEM, caPath: caPEM} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			log.ErrorContext(ctx, "embedded worker: write key material", "err", err)
			return
		}
	}

	// Every other worker knob (data dir, ansible binaries, claim
	// interval, concurrency) rides the manager process's own environment
	// — an operator who wants to tune the embedded worker sets the same
	// PULSIX_WORKER_* variables cmd/worker reads. Only the four
	// enrollment-derived values are synthesized here, appended last so
	// they win over anything (meaninglessly) set in the environment.
	env := append(os.Environ(),
		"PULSIX_MANAGER_ADDR="+managerAddr,
		"PULSIX_WORKER_CERT="+certPath,
		"PULSIX_WORKER_KEY="+keyPath,
		"PULSIX_CA_CERT="+caPath,
		"PULSIX_MANAGER_SERVER_NAME="+serverName,
	)
	wcfg, err := worker.Load(env)
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: configuration", "err", err)
		return
	}
	wcfg.Version = "embedded"

	conn, err := worker.Dial(ctx, wcfg)
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: dial manager", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()

	runExec := runner.NewRunner(wcfg.AnsibleRunnerBin, wcfg.AnsibleBin, "git", 0, log)
	probeExec := worker.NewSSHProbeExecutor(&sshprobe.Probe{}, log)
	loop := worker.NewLoop(
		wcfg, workerpb.NewWorkerServiceClient(conn),
		worker.NewCompositeExecutor(map[string]worker.Executor{
			"run": runExec, "adhoc": runExec,
			"bootstrap":       worker.NewBootstrapExecutor(runExec),
			"rotate_key":      worker.NewRotateExecutor(runExec),
			"revoke_key":      worker.NewRevokeExecutor(runExec),
			"connection_test": probeExec,
			"sync_repo":       worker.NewSyncExecutor(runExec, "ansible-galaxy"),
		}),
		worker.WithLogger(log),
	)
	log.InfoContext(ctx, "embedded worker running", "org_id", zone.OrgID, "zone_id", zone.ID)
	if err := loop.Run(ctx); err != nil {
		log.ErrorContext(ctx, "embedded worker: loop stopped", "err", err)
	}
}
