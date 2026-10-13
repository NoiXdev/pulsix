package alertroutes

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

// Window is a route's delivery window: when it MAY deliver, not when it
// may not (spec decision 2). It is stored in maintenance.Window's own
// shape — cron for the start instants, a duration to make each an
// interval, and a timezone — so maintenance.ActiveAt applies with no
// translation layer.
//
// A window crossing midnight needs no special case: quiet hours of 22:00
// to 07:00 are the cron "0 7 * * *" with a fifteen-hour duration.
type Window struct {
	Cron     string
	Duration time.Duration
	Timezone string
}

// ToMaintenance lends the window to maintenance.ActiveAt, which is the
// only thing that reads it. id only names the window in that package's
// error messages.
func (w Window) ToMaintenance(id uuid.UUID) maintenance.Window {
	return maintenance.Window{ID: id, Cron: w.Cron, Duration: w.Duration, Timezone: w.Timezone}
}

// windowEpoch is the fixed reference instant validateWindow walks forward
// from to find two consecutive occurrences. Any instant would do — Next
// only uses it to seed the search — a fixed one keeps Validate's answer
// independent of when it happens to run, rather than drifting with
// time.Now() the way a window straddling a boundary condition could.
var windowEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// validateWindow rejects a window whose cron does not parse, whose
// timezone is not a known IANA zone, whose duration is not positive, or
// whose duration exceeds the gap between two consecutive occurrences — a
// longer duration would make the window's own occurrences overlap, which
// describes something nobody meant (spec decision 2's own boundary).
func validateWindow(w *Window) error {
	if w == nil {
		return nil
	}
	expr, err := cronexpr.Parse(w.Cron)
	if err != nil {
		return fmt.Errorf("%w: invalid window cron %q: %v", ErrInvalidInput, w.Cron, err)
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return fmt.Errorf("%w: unknown window timezone %q", ErrInvalidInput, w.Timezone)
	}
	if w.Duration <= 0 {
		return fmt.Errorf("%w: window duration must be positive", ErrInvalidInput)
	}
	occ := expr.NextN(windowEpoch, loc, 2)
	if len(occ) < 2 {
		return fmt.Errorf("%w: window cron %q has no recurring occurrences", ErrInvalidInput, w.Cron)
	}
	if gap := occ[1].Sub(occ[0]); w.Duration > gap {
		return fmt.Errorf("%w: window duration %s exceeds the gap between its own occurrences (%s)", ErrInvalidInput, w.Duration, gap)
	}
	return nil
}

// validateBreakthrough rejects a breakthrough set with no window to cross
// (the same emptiness as a window with nothing crossing it, from the
// other side) and any severity that is not among sevs — a severity the
// route does not otherwise fire on would cross a closed window into
// nothing, reading as configured while being silent.
func validateBreakthrough(breakthrough []rules.State, win *Window, sevs []rules.State) error {
	if len(breakthrough) == 0 {
		return nil
	}
	if win == nil {
		return fmt.Errorf("%w: breakthrough severities need a window to cross", ErrInvalidInput)
	}
	seen := make(map[rules.State]struct{}, len(breakthrough))
	for _, b := range breakthrough {
		if !slices.Contains(routableSeverities, b) {
			return fmt.Errorf("%w: unknown breakthrough severity %v", ErrInvalidInput, b)
		}
		if _, dup := seen[b]; dup {
			return fmt.Errorf("%w: breakthrough severity %v listed twice", ErrInvalidInput, b)
		}
		seen[b] = struct{}{}
		if !slices.Contains(sevs, b) {
			return fmt.Errorf("%w: breakthrough severity %v is not among the route's own severities", ErrInvalidInput, b)
		}
	}
	return nil
}

// windowColumns maps an optional Window onto the three nullable columns
// migration 00054 added to alert_routes. All three are set together or
// left nil together — alert_routes_window_complete enforces that in the
// database, and Validate's own nil check is what keeps a caller from ever
// supplying only some of them.
func windowColumns(w *Window) (cron *string, durationSeconds *int32, timezone *string) {
	if w == nil {
		return nil, nil, nil
	}
	secs := int32(w.Duration / time.Second) //nolint:gosec // validateWindow bounds this to a cron's own recurrence gap
	return &w.Cron, &secs, &w.Timezone
}

// windowFromColumns reverses windowColumns when reading a row back. cron
// is the one column that says whether a window is set at all — the CHECK
// constraint guarantees the other two agree.
func windowFromColumns(cron *string, durationSeconds *int32, timezone *string) *Window {
	if cron == nil {
		return nil
	}
	w := &Window{Cron: *cron}
	if durationSeconds != nil {
		w.Duration = time.Duration(*durationSeconds) * time.Second
	}
	if timezone != nil {
		w.Timezone = *timezone
	}
	return w
}

// breakthroughByRoute groups every one of org's breakthrough severities by
// route id in a single round trip, selectionsByRoute's own N+1-avoiding
// reasoning (selections_sql.go) for the fourth selection table
// (ListRouteBreakthroughSeverities' own doc comment on why it stays a
// dedicated query rather than a fourth UNION arm in ListRouteSelections).
func (s *Store) breakthroughByRoute(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (map[uuid.UUID][]rules.State, error) {
	rows, err := q.ListRouteBreakthroughSeverities(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list route breakthrough severities: %w", err)
	}
	out := make(map[uuid.UUID][]rules.State, len(rows))
	for _, r := range rows {
		out[r.RouteID] = append(out[r.RouteID], rules.State(r.Severity))
	}
	return out, nil
}
