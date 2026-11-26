package app

import (
	"time"

	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/probe"
)

// buildOptions are test seams for Build; production passes none.
type buildOptions struct {
	scheduleNow func() time.Time
	// authReaperInterval overrides the expired-ceremony and expired-session
	// reaper's sweep period (0 keeps auth.DefaultReaperInterval); a test
	// seam so the app integration test can watch a sweep happen without
	// waiting an hour.
	authReaperInterval time.Duration
	// inviteReaperInterval overrides the stale-invitation purge's sweep
	// period (0 keeps tenancy.DefaultInviteReaperInterval); a test seam
	// like authReaperInterval.
	inviteReaperInterval time.Duration
	// retentionPurgeInterval overrides the retention purge's period (0
	// keeps monitorsettings.RetentionPurgeInterval); a test seam like
	// authReaperInterval.
	retentionPurgeInterval time.Duration
	// chainTick overrides the follow-up starter's interval (0 keeps
	// chain.DefaultTick); a test seam so an end-to-end test sees a chain
	// continue without waiting seconds.
	chainTick time.Duration
	// settleTick overrides the webhook settle ticker's interval (0 keeps
	// triggers.DefaultSettleTick); a test seam like chainTick. The ticker
	// reads scheduleNow, the schedule ticker's clock.
	settleTick time.Duration
	// probeGuard replaces the synthetic-check probe runner's dial guard
	// (nil keeps the production guard, probeGuardFor); probeTick and
	// probeRefresh override its tick and refresh (0 keeps
	// probes.DefaultTick and probes.DefaultRefresh). Test seams so an
	// end-to-end test can probe a listener of its own on loopback
	// without waiting for the production pace.
	probeGuard   *probe.Guard
	probeTick    time.Duration
	probeRefresh time.Duration
}

// Option customizes Build.
type Option func(*buildOptions)

// WithScheduleClock injects the schedule ticker's clock (the milestone
// E2E fires a schedule without waiting for wall-clock time).
func WithScheduleClock(now func() time.Time) Option {
	return func(o *buildOptions) { o.scheduleNow = now }
}

// WithAuthReaperInterval overrides the auth reaper's sweep period — a test
// seam; production passes no option and keeps the hourly default.
func WithAuthReaperInterval(d time.Duration) Option {
	return func(o *buildOptions) { o.authReaperInterval = d }
}

// WithInviteReaperInterval overrides the invite reaper's sweep period — a
// test seam; production passes no option and keeps the hourly default.
func WithInviteReaperInterval(d time.Duration) Option {
	return func(o *buildOptions) { o.inviteReaperInterval = d }
}

// WithRetentionPurgeInterval overrides the retention purge's period — a
// test seam; production passes no option and keeps the hourly default.
func WithRetentionPurgeInterval(d time.Duration) Option {
	return func(o *buildOptions) { o.retentionPurgeInterval = d }
}

// WithChainTick overrides the follow-up starter's interval — a test seam;
// production passes no option and keeps the 5 s default.
func WithChainTick(d time.Duration) Option {
	return func(o *buildOptions) { o.chainTick = d }
}

// WithSettleTick overrides the webhook settle ticker's interval — a test
// seam; production passes no option and keeps the 5 s default.
func WithSettleTick(d time.Duration) Option {
	return func(o *buildOptions) { o.settleTick = d }
}

// WithProbeGuard replaces the synthetic-check probe runner's dial guard —
// a TEST SEAM ONLY. The production guard is an SSRF control: it keeps
// the manager's probes off loopback, link-local and cloud metadata
// addresses and the operator's PULSIX_PROBE_DENY_CIDRS. Passing a weaker
// guard (probe.NewGuardForTest) lets probes reach those addresses, so
// cmd/manager never passes this option; only tests that probe their own
// loopback listeners do.
func WithProbeGuard(g *probe.Guard) Option {
	return func(o *buildOptions) { o.probeGuard = g }
}

// probeGuardFor is the probe runner's dial guard: the one WithProbeGuard
// injected, else the production guard — the webhook egress policy's
// refused ranges seen through cfg's NAT64 prefixes, plus
// PULSIX_PROBE_DENY_CIDRS.
func (o buildOptions) probeGuardFor(cfg config.Config) *probe.Guard {
	if o.probeGuard != nil {
		return o.probeGuard
	}
	return probe.NewGuard(cfg.ProbeDenyCIDRs, cfg.NotifyNAT64Prefixes)
}

// WithProbeTick overrides how often the probe runner looks for due checks
// — a test seam; production passes no option and keeps probes.DefaultTick.
func WithProbeTick(d time.Duration) Option {
	return func(o *buildOptions) { o.probeTick = d }
}

// WithProbeRefresh overrides how often the probe runner re-resolves the
// organizations' synthetic checks and host addresses — a test seam;
// production passes no option and keeps probes.DefaultRefresh.
func WithProbeRefresh(d time.Duration) Option {
	return func(o *buildOptions) { o.probeRefresh = d }
}
