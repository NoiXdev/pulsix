package app

import "time"

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
