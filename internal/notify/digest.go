package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

// maxDigestChannels bounds a digest's channel list.
const maxDigestChannels = 20

// weekdays are the digest's day names, Sunday first like cron.
func weekdays() []string { return []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} }

// WeekdayName is the digest day name of a cron weekday (0 = Sunday).
func WeekdayName(d int) string { return weekdays()[d%7] }

// DigestSettings is an organization's weekly digest (spec §7).
type DigestSettings struct {
	Enabled bool
	// Weekday is 0 (Sunday) to 6, as in cron.
	Weekday   int
	Time      string // HH:MM in Timezone
	Timezone  string
	Channels  []ChannelRef
	NextRunAt *time.Time
}

// DigestInput is what an operator submits; Weekday is mon..sun.
type DigestInput struct {
	Enabled  bool
	Weekday  string
	Time     string
	Timezone string
	Channels []uuid.UUID
}

// DigestStore persists the digest settings (org-scoped).
type DigestStore interface {
	// GetDigest returns the settings; ok is false when none are stored.
	GetDigest(ctx context.Context, orgID uuid.UUID) (DigestSettings, bool, error)
	PutDigest(ctx context.Context, orgID uuid.UUID, s DigestSettings, channels []uuid.UUID) error
}

// defaultDigest is what an organization without stored settings sees.
func defaultDigest() DigestSettings {
	return DigestSettings{Enabled: false, Weekday: 1, Time: "08:00", Timezone: "UTC"}
}

// digestExpr is the cron expression of a weekly digest.
func digestExpr(weekday int, at string) (cronexpr.Expr, error) {
	// time.Parse requires the whole input to match "15:04" — unlike
	// fmt.Sscanf, which happily accepts a short match against part of the
	// string (e.g. "08:0a" as 08:00, "+8:30" as 08:30) and leaves the rest
	// unchecked.
	hm, err := time.Parse("15:04", at)
	if err != nil {
		return cronexpr.Expr{}, fmt.Errorf("%w: time must be HH:MM", ErrInvalidInput)
	}
	return cronexpr.Parse(fmt.Sprintf("%d %d * * %d", hm.Minute(), hm.Hour(), weekday))
}

// NextDigestRun is the first digest occurrence strictly after now.
func NextDigestRun(weekday int, at, timezone string, now time.Time) (time.Time, error) {
	expr, err := digestExpr(weekday, at)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, timezone)
	}
	return expr.Next(now, loc), nil
}

// Digest returns the organization's digest settings (defaults when none
// are stored).
func (s *Service) Digest(ctx context.Context, orgID uuid.UUID) (DigestSettings, error) {
	d, ok, err := s.d.Digests.GetDigest(ctx, orgID)
	if err != nil {
		return DigestSettings{}, err
	}
	if !ok {
		return defaultDigest(), nil
	}
	return d, nil
}

// SetDigest validates and stores the settings; an enabled digest needs a
// channel and gets its next run computed from now.
func (s *Service) SetDigest(ctx context.Context, orgID uuid.UUID, in DigestInput) (DigestSettings, error) {
	day := -1
	for i, n := range weekdays() {
		if strings.EqualFold(in.Weekday, n) {
			day = i
		}
	}
	if day < 0 {
		return DigestSettings{}, fmt.Errorf("%w: weekday must be mon, tue, wed, thu, fri, sat or sun", ErrInvalidInput)
	}
	if in.Timezone == "" || in.Timezone == "Local" {
		return DigestSettings{}, fmt.Errorf("%w: timezone must be an IANA name such as Europe/Berlin or UTC", ErrInvalidInput)
	}
	channels := make([]uuid.UUID, 0, len(in.Channels))
	seen := map[uuid.UUID]bool{}
	for _, ch := range in.Channels {
		if !seen[ch] {
			seen[ch] = true
			channels = append(channels, ch)
		}
	}
	if len(channels) > maxDigestChannels {
		return DigestSettings{}, fmt.Errorf("%w: at most %d channels", ErrInvalidInput, maxDigestChannels)
	}
	if in.Enabled && len(channels) == 0 {
		return DigestSettings{}, fmt.Errorf("%w: an enabled digest needs at least one channel", ErrInvalidInput)
	}
	next, err := NextDigestRun(day, in.Time, in.Timezone, s.d.Now())
	if err != nil {
		return DigestSettings{}, err
	}
	settings := DigestSettings{Enabled: in.Enabled, Weekday: day, Time: in.Time, Timezone: in.Timezone}
	if in.Enabled {
		settings.NextRunAt = &next
	}
	if err := s.d.Digests.PutDigest(ctx, orgID, settings, channels); err != nil {
		return DigestSettings{}, err
	}
	return s.Digest(ctx, orgID)
}
