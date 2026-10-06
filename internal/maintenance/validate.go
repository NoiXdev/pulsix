package maintenance

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

const (
	maxNameRunes = 100
	maxNoteRunes = 1000
	minDuration  = time.Minute
	maxDuration  = 31 * 24 * time.Hour
	maxOneOff    = 366 * 24 * time.Hour
)

// normalize validates an input into the window's fields; id, org and the
// bookkeeping fields are the caller's.
func normalize(in Input, now time.Time) (Window, error) {
	var w Window
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return w, fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidInput, maxNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return w, fmt.Errorf("%w: name must not contain control characters", ErrInvalidInput)
		}
	}
	w.Name = name
	if utf8.RuneCountInString(in.Note) > maxNoteRunes {
		return w, fmt.Errorf("%w: note must be at most %d characters", ErrInvalidInput, maxNoteRunes)
	}
	w.Note = in.Note
	switch in.TargetKind {
	case TargetHost, TargetGroup, TargetMonitoringGroup:
		if in.TargetID == nil {
			return w, fmt.Errorf("%w: a %s window needs target_id", ErrInvalidInput, in.TargetKind)
		}
	case TargetOrg:
		if in.TargetID != nil {
			return w, fmt.Errorf("%w: an organization window takes no target_id", ErrInvalidInput)
		}
	default:
		return w, fmt.Errorf("%w: target_kind must be host, inventory_group, monitoring_group or org", ErrInvalidInput)
	}
	w.TargetKind, w.TargetID = in.TargetKind, in.TargetID

	reason := in.Reason
	if reason == "" {
		reason = ReasonMaintenance
	}
	switch reason {
	case ReasonMaintenance, ReasonSilence:
	default:
		return w, fmt.Errorf("%w: reason must be maintenance or silence", ErrInvalidInput)
	}
	w.Reason = reason

	cron := strings.TrimSpace(in.Cron)
	if cron == "" {
		if in.Duration != "" || in.Timezone != "" {
			return w, fmt.Errorf("%w: duration and timezone belong to a recurring window (cron)", ErrInvalidInput)
		}
		if in.StartsAt == nil || in.EndsAt == nil {
			return w, fmt.Errorf("%w: a one-off window needs starts_at and ends_at; a recurring one needs cron", ErrInvalidInput)
		}
		if !in.EndsAt.After(*in.StartsAt) || in.EndsAt.Sub(*in.StartsAt) > maxOneOff {
			return w, fmt.Errorf("%w: ends_at must be after starts_at, at most 366 days later", ErrInvalidInput)
		}
		start, end := in.StartsAt.UTC(), in.EndsAt.UTC()
		w.StartsAt, w.EndsAt = &start, &end
		return w, nil
	}
	if in.StartsAt != nil || in.EndsAt != nil {
		return w, fmt.Errorf("%w: a recurring window has no starts_at or ends_at", ErrInvalidInput)
	}
	expr, err := cronexpr.Parse(cron)
	if err != nil {
		return w, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	d, err := time.ParseDuration(in.Duration)
	if err != nil || d < minDuration || d > maxDuration {
		return w, fmt.Errorf("%w: duration must be between 1m and 744h (31 days)", ErrInvalidInput)
	}
	if d%time.Minute != 0 {
		return w, fmt.Errorf("%w: duration must be whole minutes", ErrInvalidInput)
	}
	if in.Timezone == "" || in.Timezone == "Local" {
		return w, fmt.Errorf("%w: timezone must be an IANA name such as Europe/Berlin or UTC", ErrInvalidInput)
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return w, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, in.Timezone)
	}
	if expr.Next(now, loc).IsZero() {
		return w, fmt.Errorf("%w: the expression never fires", ErrInvalidInput)
	}
	w.Cron, w.Duration, w.Timezone = expr.String(), d, in.Timezone
	return w, nil
}
