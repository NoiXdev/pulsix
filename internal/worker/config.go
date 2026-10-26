// Config loads the worker binary's settings from PULSIX_* environment
// variables. The exact names are pinned by `pulsix-manager worker add`'s
// usage note (internal/app/workeradd.go) — that printed note is the
// enrollment contract an operator pastes onto the worker host, so the names
// here must never drift from it. PULSIX_WORKER_CERT/KEY/CA_CERT stay
// supported as an all-or-none set (that bundle); when none of the three is
// set, the worker instead reads or writes its identity directory
// (PULSIX_WORKER_IDENTITY_DIR) and, on first start, enrolls with
// PULSIX_ENROLL_TOKEN — the token flow the Workers page prints.
package worker

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Environment variable names. Order matters nowhere; completeness does —
// every name the enrollment note prints must be read here.
const (
	envManagerAddr   = "PULSIX_MANAGER_ADDR"
	envCertPath      = "PULSIX_WORKER_CERT"
	envKeyPath       = "PULSIX_WORKER_KEY"
	envCAPath        = "PULSIX_CA_CERT"
	envDataDir       = "PULSIX_WORKER_DATA_DIR"
	envZones         = "PULSIX_WORKER_ZONES"
	envRunnerBin     = "PULSIX_ANSIBLE_RUNNER_BIN"
	envAnsibleBin    = "PULSIX_ANSIBLE_BIN"
	envClaimInterval = "PULSIX_CLAIM_INTERVAL"
	envMaxConcurrent = "PULSIX_MAX_CONCURRENT"
	envServerName    = "PULSIX_MANAGER_SERVER_NAME"
	envEnrollToken   = "PULSIX_ENROLL_TOKEN"
	envIdentityDir   = "PULSIX_WORKER_IDENTITY_DIR"
	envPlayUID       = "PULSIX_WORKER_PLAY_UID"
	envSetuidHelper  = "PULSIX_WORKER_SETUID_HELPER"
)

// Defaults for the optional variables.
const (
	defaultDataDir         = "/var/lib/pulsix-worker"
	defaultRunnerBin       = "ansible-runner"
	defaultAnsibleBin      = "ansible-playbook"
	defaultClaimInterval   = 3 * time.Second
	defaultMaxConcurrent   = 1
	defaultIdentityDirName = "identity"
	defaultSetuidHelper    = "/usr/local/bin/pulsix-setuid-exec"
)

// Config is the validated worker configuration. Version is the build
// version, reported to the manager on every claim; Load leaves it empty —
// cmd/worker/main.go sets it from the package-level variable ldflags stamps
// (same shape as cmd/manager).
type Config struct {
	// ManagerAddr is the manager's gRPC endpoint as host:port — no scheme:
	// the connection is always mTLS gRPC.
	ManagerAddr string
	CertPath    string
	KeyPath     string
	CAPath      string
	// ServerName overrides the TLS ServerName the client verifies the
	// manager's server certificate against (PULSIX_MANAGER_SERVER_NAME,
	// optional). Dial otherwise derives it from ManagerAddr's own host,
	// which is wrong whenever the dial address names no usable host at
	// all — a loopback dial to a bind-all listener ("127.0.0.1:8444" to a
	// manager listening on ":8444") is exactly that case: the server
	// certificate's DNS SAN comes from the manager's independently
	// configured PULSIX_GRPC_SERVER_NAME (see config.Config.CAListenHost),
	// not from the address a client happens to dial it on, so the client
	// needs this override to name the same value explicitly. The embedded
	// worker (internal/app) always sets it; a standalone worker only needs
	// it when ManagerAddr's host does not match the server certificate's
	// SAN.
	ServerName string
	// DataDir is the worker's scratch root: per-job temp dirs and the
	// persistent repo cache live under it.
	DataDir string
	// Zones are the zone ids the claim names. Empty (the normal setting)
	// means the claim sends no zone ids and the manager uses the zone the
	// worker's certificate was enrolled for. A worker may only ever claim
	// for that one zone: the manager refuses a claim naming any other zone
	// (PermissionDenied), so a non-empty value is only a self-check.
	Zones []uuid.UUID
	// AnsibleRunnerBin and AnsibleBin name the pinned binaries the runner
	// (Task 9) executes.
	AnsibleRunnerBin string
	AnsibleBin       string
	// ClaimInterval is the pause between claims when the queue is empty.
	ClaimInterval time.Duration
	// MaxConcurrent bounds jobs in flight. v0.2b runs exactly one job at a
	// time; the variable exists so the operator's intent is visible, and
	// the loop warns when it is set above the supported value.
	MaxConcurrent int
	// Version is the worker's build version, set via ldflags after Load.
	Version string
	// IdentityDir holds cert.pem, key.pem and ca.pem when the three paths
	// are not set explicitly (PULSIX_WORKER_IDENTITY_DIR, default
	// <data dir>/identity): where token enrollment writes and renewal
	// replaces the certificate.
	IdentityDir string
	// EnrollToken is the one-time token (PULSIX_ENROLL_TOKEN) the worker
	// enrolls with when no usable bundle exists yet.
	EnrollToken string
	// PlayUID (PULSIX_WORKER_PLAY_UID) is the uid ansible-playbook runs
	// under; zero, the default, means off and ansible-playbook inherits
	// the worker process's own uid. A playbook task targeting localhost
	// otherwise runs as the worker, which can read the worker's own
	// client certificate and key — this lets an operator put the playbook
	// under a second, unprivileged uid that cannot.
	PlayUID uint32
	// SetuidHelper (PULSIX_WORKER_SETUID_HELPER, default
	// /usr/local/bin/pulsix-setuid-exec) is the setuid-root program that
	// performs the drop to PlayUID; read only when PlayUID is set. The
	// worker cannot change uid itself — doing so would need CAP_SETUID,
	// and a process holding that can setuid(0), so granting it would add
	// a path to root the worker does not have today. A deployment outside
	// the Pulsix worker image points this at its own helper.
	SetuidHelper string
}

// Load parses and validates the given environment (os.Environ() in the
// binary; explicit slices in tests). Unknown variables are ignored, and
// manager-side near-misses (PULSIX_LISTEN and friends) are deliberately NOT
// honored as aliases — a manager listener address has a different meaning
// than a worker dial address and must never cross over.
func Load(env []string) (Config, error) {
	vals := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vals[k] = v
	}

	var cfg Config
	addr, err := managerAddr(vals[envManagerAddr])
	if err != nil {
		return cfg, err
	}
	cfg.ManagerAddr = addr

	cfg.DataDir = vals[envDataDir]
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir
	}
	// The certificate bundle: either all three paths (the bundle `pulsix-manager
	// worker add` prints) or none, then the identity directory token
	// enrollment fills.
	paths := [][2]string{{envCertPath, vals[envCertPath]}, {envKeyPath, vals[envKeyPath]}, {envCAPath, vals[envCAPath]}}
	set := 0
	for _, p := range paths {
		if p[1] != "" {
			set++
		}
	}
	switch set {
	case 3:
		cfg.CertPath, cfg.KeyPath, cfg.CAPath = vals[envCertPath], vals[envKeyPath], vals[envCAPath]
	case 0:
		cfg.IdentityDir = vals[envIdentityDir]
		if cfg.IdentityDir == "" {
			cfg.IdentityDir = filepath.Join(cfg.DataDir, defaultIdentityDirName)
		}
		cfg.CertPath = filepath.Join(cfg.IdentityDir, "cert.pem")
		cfg.KeyPath = filepath.Join(cfg.IdentityDir, "key.pem")
		cfg.CAPath = filepath.Join(cfg.IdentityDir, "ca.pem")
	default:
		for _, p := range paths {
			if p[1] == "" {
				return cfg, fmt.Errorf("%s is required when any of %s, %s, %s is set (set all three, or none and enroll with %s)", p[0], envCertPath, envKeyPath, envCAPath, envEnrollToken)
			}
		}
	}
	cfg.ServerName = vals[envServerName]
	cfg.EnrollToken = strings.TrimSpace(vals[envEnrollToken])

	zones, err := parseZones(vals[envZones])
	if err != nil {
		return cfg, err
	}
	cfg.Zones = zones

	cfg.AnsibleRunnerBin = orDefault(vals[envRunnerBin], defaultRunnerBin)
	cfg.AnsibleBin = orDefault(vals[envAnsibleBin], defaultAnsibleBin)

	cfg.ClaimInterval = defaultClaimInterval
	if raw := vals[envClaimInterval]; raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("%s %q is not a duration: %v", envClaimInterval, raw, err)
		}
		if d <= 0 {
			return cfg, fmt.Errorf("%s %q must be a positive duration", envClaimInterval, raw)
		}
		cfg.ClaimInterval = d
	}

	cfg.MaxConcurrent = defaultMaxConcurrent
	if raw := vals[envMaxConcurrent]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return cfg, fmt.Errorf("%s %q is not a number: %v", envMaxConcurrent, raw, err)
		}
		if n < 1 {
			return cfg, fmt.Errorf("%s %q must be at least 1", envMaxConcurrent, raw)
		}
		cfg.MaxConcurrent = n
	}

	if raw := vals[envPlayUID]; raw != "" {
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return cfg, fmt.Errorf("%s %q is not a number: %v", envPlayUID, raw, err)
		}
		// 0 is root: setting it would turn "drop privilege" into "stay
		// root", the opposite of the feature's purpose, so refuse it
		// outright rather than silently de-escalating to nothing.
		if n == 0 {
			return cfg, fmt.Errorf("%s %q must not be 0 (root is never a de-escalation)", envPlayUID, raw)
		}
		// The worker's own uid is a no-op: the operator would believe
		// the playbook runs under a separate, unprivileged account when
		// it is in fact unchanged, which is worse than not setting this
		// at all because the false belief masks the exposure.
		if uint32(n) == uint32(os.Getuid()) {
			return cfg, fmt.Errorf("%s %q must not be the worker's own uid (%d)", envPlayUID, raw, os.Getuid())
		}
		cfg.PlayUID = uint32(n)
	}

	cfg.SetuidHelper = defaultSetuidHelper
	if raw := vals[envSetuidHelper]; raw != "" {
		cfg.SetuidHelper = raw
	}
	return cfg, nil
}

// managerAddr validates the endpoint: a bare host:port, with schemes
// rejected by name so an operator pasting an https:// URL gets a fixable
// error instead of a TLS handshake mystery.
func managerAddr(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%s is required (the manager's gRPC endpoint as host:port)", envManagerAddr)
	}
	for _, scheme := range []string{"http://", "https://", "grpc://", "grpcs://", "unix://"} {
		if strings.HasPrefix(raw, scheme) {
			return "", fmt.Errorf("%s %q must be host:port — write it without the %q scheme", envManagerAddr, raw, strings.TrimSuffix(scheme, "://"))
		}
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("%s %q must be host:port: %v", envManagerAddr, raw, err)
	}
	if host == "" || port == "" {
		return "", fmt.Errorf("%s %q must be host:port — host and port are both required", envManagerAddr, raw)
	}
	return raw, nil
}

// parseZones splits the comma-separated zone list, trimming spaces around
// each id; an empty value means "whatever the certificate says" (nil).
func parseZones(raw string) ([]uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	var zones []uuid.UUID
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, fmt.Errorf("%s %q is not a valid zone id: %v", envZones, part, err)
		}
		zones = append(zones, id)
	}
	return zones, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
