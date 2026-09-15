// Package config loads manager settings from PULSIX_* environment variables.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// Config is the validated manager configuration.
type Config struct {
	Listen       string
	DatabaseURL  string
	PublicURL    string
	StatusDomain string
	MasterKey    [32]byte
	// GRPCListen is the worker gRPC endpoint's listener address
	// (PULSIX_GRPC_LISTEN, e.g. ":8444"). Empty — the default — means the
	// gRPC endpoint is disabled.
	GRPCListen string
	// RetestInterval is the connection re-test scheduler's pass period
	// (PULSIX_RETEST_INTERVAL, a Go duration; the default 1h). The daily
	// re-test's 24 h window is a plan constant, not configuration.
	RetestInterval time.Duration
	// Version is the manager's build version, exposed on /healthz. It is
	// not read from the environment: Load leaves it at its zero value, and
	// cmd/manager/main.go sets it directly from the package-level `version`
	// variable ldflags stamps at build time (see the Dockerfile's `-X
	// main.version=...` and the release workflow), after Load returns.
	Version string
}

// DefaultRetestInterval is the connection re-test scheduler's pass period.
const DefaultRetestInterval = time.Hour

// Load reads and validates configuration. getenv is injected for tests.
func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	cfg.Listen = getenv("PULSIX_LISTEN")
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	cfg.DatabaseURL = getenv("PULSIX_DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("PULSIX_DATABASE_URL is required")
	}
	cfg.PublicURL = getenv("PULSIX_PUBLIC_URL")
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return cfg, errors.New("PULSIX_PUBLIC_URL must be an absolute URL")
	}
	cfg.StatusDomain = getenv("PULSIX_STATUS_DOMAIN")
	cfg.GRPCListen = getenv("PULSIX_GRPC_LISTEN") // empty = the gRPC endpoint stays disabled
	cfg.RetestInterval = DefaultRetestInterval
	if raw := getenv("PULSIX_RETEST_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return cfg, errors.New("PULSIX_RETEST_INTERVAL must be a positive duration (e.g. 30m, 2h)")
		}
		cfg.RetestInterval = d
	}
	key, err := hex.DecodeString(getenv("PULSIX_MASTER_KEY"))
	if err != nil || len(key) != 32 {
		return cfg, fmt.Errorf("PULSIX_MASTER_KEY must be 64 hex characters (32 bytes)")
	}
	copy(cfg.MasterKey[:], key)
	return cfg, nil
}

// RPID returns the WebAuthn relying party id derived from PublicURL: the
// hostname component alone, with no scheme or port. The WebAuthn spec
// requires rpID to be a registrable domain suffix of (or equal to) the
// origin the ceremony ran at, and never carries a port.
//
// Load already rejects a PublicURL that fails to parse as an absolute URL,
// so this is only ever called on an already-validated Config; the error
// from url.Parse is deliberately ignored, same as Origin below.
func (c Config) RPID() string {
	u, _ := url.Parse(c.PublicURL)
	return u.Hostname()
}

// Origin returns the WebAuthn origin derived from PublicURL: scheme://host,
// with the port when PublicURL has one and no path — the exact form a
// browser reports as the ceremony's collected origin.
func (c Config) Origin() string {
	u, _ := url.Parse(c.PublicURL)
	return u.Scheme + "://" + u.Host
}

// GRPCHost is the hostname the gRPC server certificate carries as its DNS
// SAN: the host part of GRPCListen, or empty when the address names no
// host (":8444") — the certificate then names no host and clients must pin
// the CA alone. SplitHostPort's error is ignored the same way RPID's is:
// GRPCListen is operator-set and only read when non-empty; an address
// SplitHostPort cannot read degrades to "no SAN" rather than failing to
// start.
func (c Config) GRPCHost() string {
	host, _, err := net.SplitHostPort(c.GRPCListen)
	if err != nil {
		return ""
	}
	return host
}
