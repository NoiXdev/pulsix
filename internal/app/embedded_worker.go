// The embedded worker (PULSIX_EMBEDDED_WORKER=1, off by default): the same
// worker package cmd/worker drives, started in-process and authenticated
// over loopback mTLS. It enrolls itself much like the
// `pulsix-manager worker add` path (resolveZone from workeradd.go,
// ca.CA.Issue, then workers.Service.RegisterOrAttach, the one path the
// reserved name "embedded" may use), so an embedded worker is
// indistinguishable from one an operator enrolled by hand: same workers
// table row, same certificate serial the gRPC interceptor checks on every
// call.
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
// re-attaches to the same "embedded" worker row (workers.RegisterOrAttach;
// the name is reserved for it, so no other enrollment can take it) and
// adds one certificate row for its freshly issued key — those certificate
// rows accumulate across restarts until an operator revokes or deletes the
// worker — and a failure after zone resolution (issuing, registering,
// dialing) stops the embedded worker until the next restart. A revoked
// row, or one enrolled in another zone than the default zone, keeps it
// down with an error log naming the fix.
package app

import (
	"context"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts"
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

// startEmbeddedWorker launches the embedded worker's resolve-and-run
// goroutine and returns a stop function that cancels it and waits for the
// goroutine to actually exit — Close calls it exactly like it stops the
// reaper and the retest scheduler. managerAddr is the loopback address the
// worker dials (127.0.0.1:<gRPC port>); serverName is the TLS ServerName
// it verifies the manager's server certificate against — it must be
// exactly cfg.CAListenHost(), the same name the server certificate's SAN
// was issued with, or the loopback handshake fails (see
// internal/worker.Config.ServerName's own doc comment).
func startEmbeddedWorker(appDB *db.DB, caInst *ca.CA, workersSvc *workers.Service, managerAddr, serverName string, log *slog.Logger) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runEmbeddedWorker(ctx, appDB, caInst, workersSvc, managerAddr, serverName, log)
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
func runEmbeddedWorker(ctx context.Context, appDB *db.DB, caInst *ca.CA, workersSvc *workers.Service, managerAddr, serverName string, log *slog.Logger) {
	src := genZoneSource{q: gen.New(appDB.Pool())}
	ticker := time.NewTicker(embeddedWorkerRetryInterval)
	defer ticker.Stop()
	warned := false
	for {
		zone, err := resolveZone(ctx, src, hosts.DefaultZoneSlug, "")
		if err == nil {
			runEmbeddedLoop(ctx, caInst, workersSvc, zone, managerAddr, serverName, log)
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

// registerEmbedded records the embedded worker's freshly issued
// certificate on its row (created on the first start, reused afterwards,
// workers.RegisterOrAttach) and reports whether the worker may run. Every
// refusal is logged with what the operator has to do about it.
func registerEmbedded(ctx context.Context, workersSvc *workers.Service, zone zoneRow, serial *big.Int, log *slog.Logger) bool {
	_, err := workersSvc.RegisterOrAttach(ctx, zone.OrgID, workers.RegisterInput{
		ZoneID: zone.ID, Name: workers.EmbeddedName, CertSerial: serial,
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, workers.ErrRevoked):
		log.ErrorContext(ctx, "embedded worker: revoked; delete it on the Workers page to re-create it")
	case errors.Is(err, workers.ErrZoneMismatch):
		log.ErrorContext(ctx, "embedded worker: the existing \"embedded\" worker is enrolled in another zone; delete it on the Workers page to re-create it in the default zone",
			"org_id", zone.OrgID, "zone_id", zone.ID)
	default:
		log.ErrorContext(ctx, "embedded worker: register", "err", err)
	}
	return false
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
func runEmbeddedLoop(ctx context.Context, caInst *ca.CA, workersSvc *workers.Service, zone zoneRow, managerAddr, serverName string, log *slog.Logger) {
	certPEM, keyPEM, serial, err := caInst.Issue(ctx, &zone.OrgID, zone.ID, workers.EmbeddedName)
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: issue certificate", "err", err)
		return
	}
	if !registerEmbedded(ctx, workersSvc, zone, serial, log) {
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

	id, err := worker.LoadIdentity(wcfg.CertPath, wcfg.KeyPath)
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: load identity", "err", err)
		return
	}
	conn, err := worker.DialIdentity(ctx, wcfg, id)
	if err != nil {
		log.ErrorContext(ctx, "embedded worker: dial manager", "err", err)
		return
	}
	// client can move to a fresh connection after a certificate renewal
	// (Redial, wired below) without the loop or the renewer knowing — see
	// ReconnectingClient's own doc comment for why that matters for a
	// long-lived connection like this one. client.Close (not conn.Close,
	// which would miss a redial) closes whatever is current at shutdown.
	client := worker.NewReconnectingClient(conn, log)
	defer func() { _ = client.Close() }()
	// Renewal at start and every 6 h, once a third of the validity is
	// left; a successful renewal redials so the new certificate actually
	// reaches the wire.
	go worker.NewRenewer(client, id, nil, nil, log, worker.WithOnRenewed(func(renewCtx context.Context) {
		client.Redial(renewCtx, func(dialCtx context.Context) (*grpc.ClientConn, error) {
			return worker.DialIdentity(dialCtx, wcfg, id)
		})
	})).Run(ctx)

	runExec := runner.NewRunner(wcfg.AnsibleRunnerBin, wcfg.AnsibleBin, "git", 0, log)
	probeExec := worker.NewSSHProbeExecutor(&sshprobe.Probe{}, log)
	loop := worker.NewLoop(
		wcfg, client,
		worker.NewCompositeExecutor(map[string]worker.Executor{
			"run": runExec, "adhoc": runExec,
			"bootstrap":       worker.NewBootstrapExecutor(runExec),
			"rotate_key":      worker.NewRotateExecutor(runExec),
			"revoke_key":      worker.NewRevokeExecutor(runExec),
			"connection_test": probeExec,
			"sync_repo":       worker.NewSyncExecutor(runExec, "ansible-galaxy"),
		}),
		worker.WithLogger(log),
		worker.WithIdentity(id),
	)
	log.InfoContext(ctx, "embedded worker running", "org_id", zone.OrgID, "zone_id", zone.ID)
	if err := loop.Run(ctx); err != nil {
		log.ErrorContext(ctx, "embedded worker: loop stopped", "err", err)
	}
}
