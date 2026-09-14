// Config loads the worker binary's settings from PULSIX_* environment
// variables. The exact names are pinned by `pulsix-manager worker add`'s
// usage note (internal/app/workeradd.go) — that printed note is the
// enrollment contract an operator pastes onto the worker host, so the names
// here must never drift from it.
package worker

import (
	"fmt"
	"net"
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
)

// Defaults for the optional variables.
const (
	defaultDataDir       = "/var/lib/pulsix-worker"
	defaultRunnerBin     = "ansible-runner"
	defaultAnsibleBin    = "ansible-playbook"
	defaultClaimInterval = 3 * time.Second
	defaultMaxConcurrent = 1
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
	// DataDir is the worker's scratch root: per-job temp dirs and the
	// persistent repo cache live under it.
	DataDir string
	// Zones are the zone ids to claim for. Empty means the claim sends no
	// zone ids and the manager resolves them from the certificate's
	// urn:pulsix:zone SAN.
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
	for _, pair := range [][2]string{
		{envCertPath, vals[envCertPath]},
		{envKeyPath, vals[envKeyPath]},
		{envCAPath, vals[envCAPath]},
	} {
		if pair[1] == "" {
			return cfg, fmt.Errorf("%s is required (the worker needs its mTLS material; see `pulsix-manager worker add`)", pair[0])
		}
	}
	cfg.CertPath = vals[envCertPath]
	cfg.KeyPath = vals[envKeyPath]
	cfg.CAPath = vals[envCAPath]

	cfg.DataDir = vals[envDataDir]
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir
	}

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
