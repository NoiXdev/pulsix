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
