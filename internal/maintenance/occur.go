package maintenance

import (
	"fmt"
	"slices"
	"time"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

// timetable parses a recurring window's expression and zone.
func (w Window) timetable() (cronexpr.Expr, *time.Location, error) {
	expr, err := cronexpr.Parse(w.Cron)
	if err != nil {
		return cronexpr.Expr{}, nil, err
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return cronexpr.Expr{}, nil, fmt.Errorf("unknown timezone %q: %w", w.Timezone, err)
	}
	return expr, loc, nil
}

// ActiveAt reports whether w covers the instant at. A recurring window is
// active when an occurrence started in (at - Duration, at]: the first
// occurrence strictly after at - Duration, if it is not after at.
func ActiveAt(w Window, at time.Time) (bool, error) {
	if !w.Recurring() {
		if w.StartsAt == nil || w.EndsAt == nil {
			return false, fmt.Errorf("one-off window %s has no start or end", w.ID)
		}
		return !at.Before(*w.StartsAt) && at.Before(*w.EndsAt), nil
	}
	expr, loc, err := w.timetable()
	if err != nil {
		return false, err
	}
	s := expr.Next(at.Add(-w.Duration), loc)
	return !s.IsZero() && !s.After(at), nil
}

// Occurrences lists the stretches of w that overlap [from, to), at most
// max of them, in order.
func Occurrences(w Window, from, to time.Time, max int) ([]Occurrence, error) {
	occ := func(start, end time.Time) Occurrence {
		return Occurrence{WindowID: w.ID, Name: w.Name, TargetKind: w.TargetKind, TargetID: w.TargetID, TargetName: w.TargetName, Start: start, End: end}
	}
	if !w.Recurring() {
		if w.StartsAt == nil || w.EndsAt == nil {
			return nil, fmt.Errorf("one-off window %s has no start or end", w.ID)
		}
		if w.StartsAt.Before(to) && w.EndsAt.After(from) {
			return []Occurrence{occ(*w.StartsAt, *w.EndsAt)}, nil
		}
		return nil, nil
	}
	expr, loc, err := w.timetable()
	if err != nil {
		return nil, err
	}
	var out []Occurrence
	for s := expr.Next(from.Add(-w.Duration), loc); !s.IsZero() && s.Before(to) && len(out) < max; s = expr.Next(s, loc) {
		out = append(out, occ(s, s.Add(w.Duration)))
	}
	return out, nil
}

// sortOccurrences orders occurrences by start, then window id.
func sortOccurrences(occ []Occurrence) {
	slices.SortFunc(occ, func(a, b Occurrence) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return slices.Compare(a.WindowID[:], b.WindowID[:])
	})
}
