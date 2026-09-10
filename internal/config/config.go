// Package config loads manager settings from PULSIX_* environment variables.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
)

// Config is the validated manager configuration.
type Config struct {
	Listen       string
	DatabaseURL  string
	PublicURL    string
	StatusDomain string
	MasterKey    [32]byte
}

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
