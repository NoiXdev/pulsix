// Package config loads manager settings from PULSIX_* environment variables.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/outbound"
	"github.com/noixdev/pulsix/internal/probe"
)

// defaultCAListenHost is the DNS SAN the gRPC server certificate falls
// back to when neither PULSIX_GRPC_SERVER_NAME nor GRPCListen's own host
// part name one — a bind-all listener like ":8444" has no host at all.
// It must match internal/app's caCommonName (the CA's CommonName): both
// name "the manager", and keeping them identical means a loopback client
// that pins the CommonName-shaped default needs no separate constant.
const defaultCAListenHost = "pulsix-manager"

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
	// GRPCServerName overrides the gRPC server certificate's DNS SAN
	// (PULSIX_GRPC_SERVER_NAME), independent of what GRPCListen's own host
	// part says — useful precisely when GRPCListen binds every interface
	// (":8444") and therefore names no host of its own. See CAListenHost.
	GRPCServerName string
	// GRPCPublicAddr is the host:port remote workers dial
	// (PULSIX_GRPC_PUBLIC_ADDR). Default: PUBLIC_URL's host with
	// GRPCListen's port; empty while the gRPC endpoint is disabled. The
	// Workers page puts it into the enrollment snippets.
	GRPCPublicAddr string
	// EmbeddedWorker runs an in-process worker inside the manager
	// (PULSIX_EMBEDDED_WORKER, a bool, default false). Opt-in only, for
	// trusted single-tenant setups: its playbooks run next to the
	// manager's master key and database credentials (spec §2/§16 — the
	// default deployment runs a separate worker container instead). It
	// requires GRPCListen to be set: the embedded worker is just another
	// gRPC client, dialing the manager's own endpoint over loopback.
	EmbeddedWorker bool
	// RetestInterval is the connection re-test scheduler's pass period
	// (PULSIX_RETEST_INTERVAL, a Go duration; the default 1h). The daily
	// re-test's 24 h window is a plan constant, not configuration.
	RetestInterval time.Duration
	// ItemExpiry is how long a check's item may stay silent, while its host
	// keeps reporting, before the manager forgets it (PULSIX_ITEM_EXPIRY, a
	// Go duration of at least MinItemExpiry, 48h; the default 168h; 0
	// switches the automatic expiry off).
	ItemExpiry time.Duration
	// SampleRetentionDays is the instance maximum, in whole days, for
	// keeping measurements — check_samples and check_state_changes
	// (PULSIX_SAMPLE_RETENTION, MinSampleRetentionDays to
	// MaxSampleRetentionDays, default DefaultSampleRetentionDays; backlog
	// 3.15). The manager sets both hypertables' retention policy to it at
	// start, and no organization keeps measurements longer. Read it through
	// RetentionMaxDays, which also covers a hand-built Config.
	SampleRetentionDays int
	// SampleRetentionSet reports that PULSIX_SAMPLE_RETENTION was set
	// (false: the default applies), so a restore can say which it applied.
	SampleRetentionSet bool
	// JobMaxAttempts is how often a job may be claimed before the reaper
	// fails a stale claim instead of requeuing it (PULSIX_JOB_MAX_ATTEMPTS,
	// a positive integer; default DefaultJobMaxAttempts). It stops a job
	// whose claim a worker can never complete from looping forever.
	JobMaxAttempts int
	// ScheduleTick is how often the schedule ticker looks for due
	// schedules (PULSIX_SCHEDULE_TICK, default 15s, minimum 5s). Twice the
	// tick is the grace period after which a due run counts as late.
	ScheduleTick time.Duration
	// AuthRateLimit is how many failed authentication attempts one client
	// address may make per minute across each of the public auth
	// endpoints — login, TOTP verify, passkey login finish and invite
	// accept (PULSIX_AUTH_RATE_LIMIT, a non-negative integer; default
	// DefaultAuthRateLimit). Each endpoint counts its own budget, and only
	// failed attempts are counted, so ordinary sign-ins are never charged.
	// 0 disables the limiter — that is what the tests use, not a
	// production setting; production should keep the default or set a
	// value its user count can live with.
	AuthRateLimit int
	// TrustedProxies are the reverse proxies whose X-Forwarded-For the
	// API believes (PULSIX_TRUSTED_PROXIES, CIDRs or addresses, comma
	// separated, default empty: the TCP peer is the client).
	TrustedProxies []netip.Prefix
	// NotifyAllowCIDRs re-admits destination ranges notification webhooks
	// are otherwise refused (loopback, link-local, metadata; see
	// internal/outbound) — PULSIX_NOTIFY_ALLOW_CIDRS, CIDRs or addresses,
	// comma separated, default empty.
	NotifyAllowCIDRs []netip.Prefix
	// NotifyNAT64Prefixes are the operator's own NAT64 translation
	// prefixes (PULSIX_NOTIFY_NAT64_PREFIXES, RFC 6052 network-specific
	// prefixes, comma separated, default empty). Without them an
	// address inside such a prefix looks like ordinary global space, so
	// a deployment running its own NAT64 had a route to any blocked
	// IPv4 address the outbound policy could not recognise as one
	// (backlog 2.76). The well-known 64:ff9b::/96 needs no
	// configuration. Only the six prefix lengths RFC 6052 §2.2 defines
	// an embedding for are accepted.
	NotifyNAT64Prefixes []netip.Prefix
	// ProbeDenyCIDRs are destination ranges the synthetic-check probes
	// must never dial, on top of the manager's own loopback, link-local and
	// cloud metadata addresses, which are always refused (the same list
	// notification webhooks refuse, seen through
	// PULSIX_NOTIFY_NAT64_PREFIXES; PULSIX_NOTIFY_ALLOW_CIDRS does not
	// re-admit any of them for a probe) (PULSIX_PROBE_DENY_CIDRS, CIDRs
	// comma separated, default empty; internal/probe's guard). The
	// compose README recommends adding the stack's own network. An
	// invalid entry fails start-up naming it.
	ProbeDenyCIDRs []netip.Prefix
	// Version is the manager's build version, exposed on /healthz. It is
	// not read from the environment: Load leaves it at its zero value, and
	// cmd/manager/main.go sets it directly from the package-level `version`
	// variable ldflags stamps at build time (see the Dockerfile's `-X
	// main.version=...` and the release workflow), after Load returns.
	Version string
}

// DefaultItemExpiry is the default PULSIX_ITEM_EXPIRY; MinItemExpiry is the
// shortest period accepted: twice the longest check interval (48h), so an
// item of a check that reports once a day is never forgotten between two
// reports — or after missing one — and then rediscovered, with its alerts
// closed and reopened, every day.
const (
	DefaultItemExpiry = 168 * time.Hour
	MinItemExpiry     = 2 * checks.MaxInterval
)

// DefaultSampleRetentionDays is the default PULSIX_SAMPLE_RETENTION —
// the 90 days migrations 00044 and 00046 set, so nothing changes without
// the variable; MinSampleRetentionDays and MaxSampleRetentionDays bound it.
const (
	DefaultSampleRetentionDays = 90
	MinSampleRetentionDays     = 7
	MaxSampleRetentionDays     = 365
)

// DefaultRetestInterval is the connection re-test scheduler's pass period.
const DefaultRetestInterval = time.Hour

// DefaultJobMaxAttempts is the default PULSIX_JOB_MAX_ATTEMPTS.
const DefaultJobMaxAttempts = 3

// DefaultScheduleTick and MinScheduleTick bound PULSIX_SCHEDULE_TICK.
const (
	DefaultScheduleTick = 15 * time.Second
	MinScheduleTick     = 5 * time.Second
)

// DefaultAuthRateLimit is the default PULSIX_AUTH_RATE_LIMIT: failed auth
// attempts per minute per address, per endpoint. Generous enough that a
// mistyped password a few times never trips it, tight enough that a
// credential-stuffing run stalls out.
const DefaultAuthRateLimit = 20

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
	if err := validatePublicURL(cfg.PublicURL); err != nil {
		return cfg, err
	}
	cfg.StatusDomain = getenv("PULSIX_STATUS_DOMAIN")
	cfg.GRPCListen = getenv("PULSIX_GRPC_LISTEN") // empty = the gRPC endpoint stays disabled
	cfg.GRPCServerName = getenv("PULSIX_GRPC_SERVER_NAME")
	if cfg.GRPCListen != "" {
		cfg.GRPCPublicAddr = getenv("PULSIX_GRPC_PUBLIC_ADDR")
		if cfg.GRPCPublicAddr == "" {
			cfg.GRPCPublicAddr = defaultGRPCPublicAddr(cfg.PublicURL, cfg.GRPCListen)
			// Derivation failed (no usable port on the listener): blame
			// the variable the operator actually set — PULSIX_GRPC_LISTEN,
			// not the PULSIX_GRPC_PUBLIC_ADDR they never wrote (backlog
			// 2.66).
			if cfg.GRPCPublicAddr == "" {
				return cfg, fmt.Errorf("PULSIX_GRPC_LISTEN %q has no port to derive a public worker address from", cfg.GRPCListen)
			}
		} else if host, _, err := net.SplitHostPort(cfg.GRPCPublicAddr); err != nil || host == "" {
			// A bare ":443" splits fine but names no host — a public
			// address remote workers dial must carry one.
			return cfg, fmt.Errorf("PULSIX_GRPC_PUBLIC_ADDR %q must be host:port with a non-empty host", cfg.GRPCPublicAddr)
		}
	}
	if raw := getenv("PULSIX_EMBEDDED_WORKER"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return cfg, fmt.Errorf("PULSIX_EMBEDDED_WORKER %q is not a boolean: %v", raw, err)
		}
		cfg.EmbeddedWorker = b
	}
	if cfg.EmbeddedWorker && cfg.GRPCListen == "" {
		return cfg, errors.New("PULSIX_EMBEDDED_WORKER requires PULSIX_GRPC_LISTEN to be set")
	}
	cfg.RetestInterval = DefaultRetestInterval
	if raw := getenv("PULSIX_RETEST_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return cfg, errors.New("PULSIX_RETEST_INTERVAL must be a positive duration (e.g. 30m, 2h)")
		}
		cfg.RetestInterval = d
	}
	cfg.ItemExpiry = DefaultItemExpiry
	if raw := getenv("PULSIX_ITEM_EXPIRY"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 || (d != 0 && d < MinItemExpiry) {
			return cfg, fmt.Errorf("PULSIX_ITEM_EXPIRY must be a duration of at least %s (e.g. 72h, 168h) or 0 to disable", fmtHours(MinItemExpiry))
		}
		cfg.ItemExpiry = d
	}
	cfg.SampleRetentionDays = DefaultSampleRetentionDays
	if raw := getenv("PULSIX_SAMPLE_RETENTION"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < MinSampleRetentionDays || n > MaxSampleRetentionDays {
			return cfg, fmt.Errorf("PULSIX_SAMPLE_RETENTION must be a whole number of days from %d to %d (e.g. 30, 90, 365)", MinSampleRetentionDays, MaxSampleRetentionDays)
		}
		cfg.SampleRetentionDays = n
		cfg.SampleRetentionSet = true
	}
	cfg.JobMaxAttempts = DefaultJobMaxAttempts
	if raw := getenv("PULSIX_JOB_MAX_ATTEMPTS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return cfg, errors.New("PULSIX_JOB_MAX_ATTEMPTS must be a positive integer")
		}
		cfg.JobMaxAttempts = n
	}
	cfg.ScheduleTick = DefaultScheduleTick
	if raw := getenv("PULSIX_SCHEDULE_TICK"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < MinScheduleTick {
			return cfg, fmt.Errorf("PULSIX_SCHEDULE_TICK must be a duration of at least %s (e.g. 15s)", MinScheduleTick)
		}
		cfg.ScheduleTick = d
	}
	cfg.AuthRateLimit = DefaultAuthRateLimit
	if raw := getenv("PULSIX_AUTH_RATE_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("PULSIX_AUTH_RATE_LIMIT must be a non-negative integer (failed attempts per minute per address; 0 disables the limit, for tests)")
		}
		cfg.AuthRateLimit = n
	}
	proxies, err := clientip.ParsePrefixes(getenv("PULSIX_TRUSTED_PROXIES"))
	if err != nil {
		return cfg, fmt.Errorf("PULSIX_TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = proxies
	notifyAllow, err := clientip.ParsePrefixes(getenv("PULSIX_NOTIFY_ALLOW_CIDRS"))
	if err != nil {
		return cfg, fmt.Errorf("PULSIX_NOTIFY_ALLOW_CIDRS: %w", err)
	}
	cfg.NotifyAllowCIDRs = notifyAllow
	nat64, err := clientip.ParsePrefixes(getenv("PULSIX_NOTIFY_NAT64_PREFIXES"))
	if err != nil {
		return cfg, fmt.Errorf("PULSIX_NOTIFY_NAT64_PREFIXES: %w", err)
	}
	for _, pre := range nat64 {
		if !outbound.ValidNAT64Prefix(pre) {
			return cfg, fmt.Errorf("PULSIX_NOTIFY_NAT64_PREFIXES: %s is not a NAT64 prefix: RFC 6052 defines an embedding only for an IPv6 /32, /40, /48, /56, /64 or /96", pre)
		}
	}
	cfg.NotifyNAT64Prefixes = nat64
	probeDeny, err := probe.ParseCIDRs(getenv("PULSIX_PROBE_DENY_CIDRS"))
	if err != nil {
		return cfg, fmt.Errorf("PULSIX_PROBE_DENY_CIDRS: %w", err)
	}
	cfg.ProbeDenyCIDRs = probeDeny
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

// RetentionMaxDays is the instance maximum retention for measurements in
// days: SampleRetentionDays, or DefaultSampleRetentionDays when it is zero
// — Load always sets it, but a Config built by hand (the tests do) must
// not hand the policy sync, or the per-organization deletion, a maximum of
// zero days.
func (c Config) RetentionMaxDays() int {
	if c.SampleRetentionDays <= 0 {
		return DefaultSampleRetentionDays
	}
	return c.SampleRetentionDays
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

// CAListenHost is the name Build actually issues the gRPC server
// certificate's DNS SAN with: GRPCServerName when the operator set one,
// otherwise GRPCHost's derived host, otherwise defaultCAListenHost — so a
// bind-all listener (the common case, ":8444") still produces a
// certificate a client can verify against with an explicit ServerName
// (internal/worker's PULSIX_MANAGER_SERVER_NAME), rather than a SAN-less
// certificate no ServerName could ever match.
func (c Config) CAListenHost() string {
	if c.GRPCServerName != "" {
		return c.GRPCServerName
	}
	if h := c.GRPCHost(); h != "" {
		return h
	}
	return defaultCAListenHost
}

// defaultGRPCPublicAddr joins the public URL's host with the gRPC
// listener's port.
func defaultGRPCPublicAddr(publicURL, listen string) string {
	u, err := url.Parse(publicURL)
	_, port, perr := net.SplitHostPort(listen)
	if err != nil || perr != nil || u.Hostname() == "" {
		return ""
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// fmtHours renders a whole number of hours the way the setting is written
// ("48h"), not time.Duration's "48h0m0s".
func fmtHours(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
}

// validatePublicURL enforces the PULSIX_PUBLIC_URL rule. The value is
// pasted into the agent install commands an administrator runs, so it is
// held to a small safe alphabet: absolute lower-case http(s), a host, no
// user info, query or fragment (@ ? # are outside the alphabet), and only
// [A-Za-z0-9._~:/%+=,-] plus the brackets of an IPv6 host literal. Errors
// name the character and its position, never the value: a refused
// user:pass@host must not put the password into the startup log.
func validatePublicURL(raw string) error {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return errors.New("PULSIX_PUBLIC_URL must be an absolute URL starting with http:// or https:// (lower-case)")
	}
	for i, r := range raw {
		if r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("._~:/%+=,-[]", r)) {
			continue
		}
		return fmt.Errorf("PULSIX_PUBLIC_URL contains %q at byte %d, which is not allowed (only letters, digits and . _ ~ : / %% + = , - and [ ] around an IPv6 host)", r, i)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return errors.New("PULSIX_PUBLIC_URL must be an absolute URL with a host")
	}
	// Brackets belong to an IPv6 host literal only; curl would glob them
	// in a path.
	rest := raw[strings.Index(raw, "://")+3:]
	if strings.ContainsAny(rest, "[]") {
		host, path, _ := strings.Cut(rest, "/")
		bracketed := strings.HasPrefix(host, "[")
		if strings.ContainsAny(path, "[]") || !bracketed {
			return errors.New("PULSIX_PUBLIC_URL may use [ ] only around an IPv6 host")
		}
		addr, err := netip.ParseAddr(u.Hostname())
		if err != nil || !addr.Is6() || !strings.HasPrefix(host, "["+u.Hostname()+"]") {
			return errors.New("PULSIX_PUBLIC_URL may use [ ] only around an IPv6 host")
		}
	}
	return nil
}
