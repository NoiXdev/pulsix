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
// max of them, in order. The second result says the cap bit, so a caller
// can tell "this window has no more stretches in the range" from "this is
// as many as you may have" — the calendar reported the first while
// meaning the second, and drew the rest of the range as free of
// maintenance (final review, Important #3).
func Occurrences(w Window, from, to time.Time, max int) ([]Occurrence, bool, error) {
	occ := func(start, end time.Time) Occurrence {
		return Occurrence{WindowID: w.ID, Name: w.Name, TargetKind: w.TargetKind, TargetID: w.TargetID, TargetName: w.TargetName, Start: start, End: end}
	}
	if !w.Recurring() {
		if w.StartsAt == nil || w.EndsAt == nil {
			return nil, false, fmt.Errorf("one-off window %s has no start or end", w.ID)
		}
		if w.StartsAt.Before(to) && w.EndsAt.After(from) {
			return []Occurrence{occ(*w.StartsAt, *w.EndsAt)}, false, nil
		}
		return nil, false, nil
	}
	expr, loc, err := w.timetable()
	if err != nil {
		return nil, false, err
	}
	var out []Occurrence
	next := expr.Next(from.Add(-w.Duration), loc)
	for ; !next.IsZero() && next.Before(to) && len(out) < max; next = expr.Next(next, loc) {
		out = append(out, occ(next, next.Add(w.Duration)))
	}
	// The loop ends on the cap only while a further stretch still starts
	// inside the range; an exhausted timetable is not a truncation.
	return out, len(out) == max && !next.IsZero() && next.Before(to), nil
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
