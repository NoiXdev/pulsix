// Config loads the agent binary's settings from PULSIX_* environment
// variables (v0.3a, spec §8). The agent is the lightweight host daemon the
// install script deploys: it enrolls with a one-time pxa1 token on first
// start, persists its identity in the data dir and then keeps one
// AgentStream open to the manager — heartbeats and facts.
//
// PULSIX_ENROLL_TOKEN is REQUIRED ONLY when no valid identity exists in
// the data dir: whether that is the case cannot be known while loading the
// environment (the data dir may not exist yet), so Config keeps the token
// optional and the loop decides. A garbage token likewise passes Load
// untouched — the manager's own enrollment path is what reports it.
//
// The exact names are the contract the install flow (deploy/install.sh)
// and the Agents page's snippets print; they must never drift from it.
package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Environment variable names.
const (
	envManagerAddr       = "PULSIX_MANAGER_ADDR"
	envEnrollToken       = "PULSIX_ENROLL_TOKEN"
	envAgentName         = "PULSIX_AGENT_NAME"
	envDataDir           = "PULSIX_DATA_DIR"
	envServerName        = "PULSIX_MANAGER_SERVER_NAME"
	envHeartbeatInterval = "PULSIX_HEARTBEAT_INTERVAL"
	envFactsInterval     = "PULSIX_FACTS_INTERVAL"
)

// Defaults and clamps. PULSIX_DATA_DIR defaults to /etc/pulsix (the agent
// writes only its identity files there — agent.crt, agent.key, agent.ca.pem
// and agent.json — unlike the worker's richer scratch layout), and the two
// intervals are clamped, not rejected: an operator's typo'd "5" (Go reads a
// bare number as nanoseconds) must not turn a heartbeat off or into a
// busy loop.
const (
	defaultDataDir = "/etc/pulsix"

	minHeartbeatInterval = 5 * time.Second
	maxHeartbeatInterval = 10 * time.Minute

	minFactsInterval = 30 * time.Minute
	maxFactsInterval = 24 * time.Hour
)

// The identity file names, inside DataDir.
const (
	certFileName = "agent.crt"
	keyFileName  = "agent.key"
	caFileName   = "agent.ca.pem"
	metaFileName = "agent.json"
)

// Config is the validated agent configuration. Version is the build
// version, reported on every heartbeat and enrollment; Load leaves it
// empty — cmd/agent/main.go sets it from the package-level variable
// ldflags stamps (same shape as cmd/manager and cmd/worker).
type Config struct {
	// ManagerAddr is the manager's gRPC endpoint as host:port — no scheme:
	// the connection is always mTLS gRPC.
	ManagerAddr string
	// ServerName overrides the TLS ServerName the agent verifies the
	// manager's server certificate against (PULSIX_MANAGER_SERVER_NAME,
	// optional — the same need the worker's identical variable covers:
	// the server certificate's DNS SAN comes from the manager's own
	// PULSIX_GRPC_SERVER_NAME, not from the address the agent happens to
	// dial).
	ServerName string
	// EnrollToken is the one-time pxa1 token (PULSIX_ENROLL_TOKEN) the
	// agent enrolls with when no valid identity exists yet. Empty is fine
	// — the loop enrolls only when it must, and reconnects never
	// re-enroll.
	EnrollToken string
	// AgentName is the name the agent presents (PULSIX_AGENT_NAME,
	// default the OS hostname). The token's name hint wins on the manager
	// side; the name still names the certificate when none is set.
	AgentName string
	// DataDir is where the identity lives (PULSIX_DATA_DIR, default
	// /etc/pulsix): agent.crt, agent.key, agent.ca.pem and agent.json.
	DataDir string
	// CertPath, KeyPath, MetaPath are the identity file paths, derived
	// from DataDir.
	CertPath string
	KeyPath  string
	MetaPath string
	// HeartbeatInterval is the AgentStream heartbeat cadence
	// (PULSIX_HEARTBEAT_INTERVAL, default 30 s, clamped into
	// [5 s, 10 min]).
	HeartbeatInterval time.Duration
	// FactsInterval is how often facts are re-collected and pushed
	// (PULSIX_FACTS_INTERVAL, default 6 h, clamped into [30 min, 24 h]).
	FactsInterval time.Duration
	// Version is the agent's build version, set via ldflags after Load.
	Version string
}

// Load parses and validates the given environment (os.Environ() in the
// binary; explicit slices in tests). Unknown variables are ignored.
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
	cfg.ServerName = vals[envServerName]
	cfg.EnrollToken = strings.TrimSpace(vals[envEnrollToken])

	cfg.AgentName = vals[envAgentName]
	if cfg.AgentName == "" {
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			return cfg, fmt.Errorf("%s is unset and the OS hostname is unusable (%v) — set the agent name explicitly", envAgentName, err)
		}
		cfg.AgentName = hostname
	}
	// The name goes behind the certificate's "pulsix-agent " common name
	// (the same 1-64-rune shape every enrollment path enforces); refusing
	// it here fails fast with the fix instead of a manager-side refusal
	// after the install script has already run.
	if n := len([]rune(cfg.AgentName)); n > 64 {
		return cfg, fmt.Errorf("%s %q is %d characters, more than the certificate's 64 — set a shorter agent name", envAgentName, cfg.AgentName, n)
	}

	cfg.DataDir = vals[envDataDir]
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir
	}
	cfg.CertPath = filepath.Join(cfg.DataDir, certFileName)
	cfg.KeyPath = filepath.Join(cfg.DataDir, keyFileName)
	cfg.MetaPath = filepath.Join(cfg.DataDir, metaFileName)

	cfg.HeartbeatInterval, err = clampedInterval(envHeartbeatInterval, vals[envHeartbeatInterval],
		30*time.Second, minHeartbeatInterval, maxHeartbeatInterval)
	if err != nil {
		return cfg, err
	}
	cfg.FactsInterval, err = clampedInterval(envFactsInterval, vals[envFactsInterval],
		6*time.Hour, minFactsInterval, maxFactsInterval)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

// managerAddr validates the endpoint: a bare host:port, with schemes
// rejected by name so an operator pasting an https:// URL gets a fixable
// error instead of a TLS handshake mystery. Same rules as the worker's.
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

// clampedInterval parses one PULSIX_*_INTERVAL value: unset keeps def,
// unparsable is an error naming the variable, and anything outside
// [min, max] is clamped to the nearer bound (never silently disabled or
// turned into a busy loop).
func clampedInterval(name, raw string, def, min, max time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a duration (e.g. 30s, 10m): %v", name, raw, err)
	}
	if d < min {
		return min, nil
	}
	if d > max {
		return max, nil
	}
	return d, nil
}
