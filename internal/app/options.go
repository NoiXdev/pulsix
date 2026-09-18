package app

import "time"

// buildOptions are test seams for Build; production passes none.
type buildOptions struct {
	scheduleNow func() time.Time
}

// Option customizes Build.
type Option func(*buildOptions)

// WithScheduleClock injects the schedule ticker's clock (the milestone
// E2E fires a schedule without waiting for wall-clock time).
func WithScheduleClock(now func() time.Time) Option {
	return func(o *buildOptions) { o.scheduleNow = now }
}
