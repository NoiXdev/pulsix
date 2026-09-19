package schedules

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

const (
	maxNameRunes     = 100
	defaultWindow    = "1h"
	minCatchUpWindow = time.Minute
	maxCatchUpWindow = 24 * time.Hour
)

// timetable is a validated expression in its zone.
type timetable struct {
	expr   cronexpr.Expr
	loc    *time.Location
	cron   string
	simple *cronexpr.Simple
}

func compileTimetable(mode Mode, cron string, simple *cronexpr.Simple, tz string, now time.Time) (timetable, error) {
	var tt timetable
	switch mode {
	case ModeCron:
		tt.cron = strings.TrimSpace(cron)
	case ModeSimple:
		if simple == nil {
			return tt, fmt.Errorf("%w: simple mode needs its form input", ErrInvalidInput)
		}
		c, err := simple.Compile()
		if err != nil {
			return tt, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		s := *simple
		tt.cron, tt.simple = c, &s
	default:
		return tt, fmt.Errorf("%w: mode must be cron or simple", ErrInvalidInput)
	}
	expr, err := cronexpr.Parse(tt.cron)
	if err != nil {
		return tt, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if tz == "" || tz == "Local" {
		return tt, fmt.Errorf("%w: timezone must be an IANA name such as Europe/Berlin or UTC", ErrInvalidInput)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return tt, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, tz)
	}
	if expr.Next(now, loc).IsZero() {
		return tt, fmt.Errorf("%w: the expression never fires", ErrInvalidInput)
	}
	tt.expr, tt.loc = expr, loc
	return tt, nil
}

func normalizeName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > maxNameRunes {
		return "", fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidInput, maxNameRunes)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: name must not contain control characters", ErrInvalidInput)
		}
	}
	return n, nil
}

// defaultWindowDuration is CatchUp.window()'s fallback when Window is
// unset or fails to parse: defaultWindow ("1h") always parses, so this
// constant, not a second parse of it, is what that fallback actually is.
const defaultWindowDuration = time.Hour

// window is the catch-up window as a duration: the stored value (already
// validated by normalizeGuards to be between 1m and 24h), defaulting to
// defaultWindowDuration when unset or unparsable and clamped to
// maxCatchUpWindow.
func (c CatchUp) window() time.Duration {
	d, err := time.ParseDuration(c.Window)
	if err != nil || d <= 0 {
		d = defaultWindowDuration
	}
	if d > maxCatchUpWindow {
		d = maxCatchUpWindow
	}
	return d
}

func normalizeGuards(g Guards) (Guards, error) {
	switch g.Overlap {
	case "":
		g.Overlap = OverlapSkip
	case OverlapSkip, OverlapQueue:
	default:
		return g, fmt.Errorf("%w: guards.overlap must be skip or queue", ErrInvalidInput)
	}
	switch g.Maintenance {
	case "":
		g.Maintenance = MaintenanceIgnore
	case MaintenanceIgnore, MaintenanceSkip, MaintenanceExcludeHosts:
	default:
		return g, fmt.Errorf("%w: guards.maintenance must be ignore, skip or exclude_hosts", ErrInvalidInput)
	}
	if g.CatchUp.Window == "" {
		g.CatchUp.Window = defaultWindow
	}
	d, err := time.ParseDuration(g.CatchUp.Window)
	if err != nil || d < minCatchUpWindow || d > maxCatchUpWindow {
		return g, fmt.Errorf("%w: guards.catch_up.window must be a duration between 1m and 24h", ErrInvalidInput)
	}
	return g, nil
}
