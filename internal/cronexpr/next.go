package cronexpr

import "time"

// searchYears bounds Next: an expression with no occurrence in five years
// (Feb 30) never fires. Five years include a Feb 29 in every case except a
// window that straddles a skipped centennial leap year (the next one is
// 2100); that is rare enough in practice to accept as-is.
const searchYears = 5

// Next returns the first occurrence strictly after `after`, evaluated in
// loc (nil is UTC), or the zero time when there is none within five years.
// The walk runs over wall-clock minutes (carried in UTC fields, so no DST
// arithmetic distorts it) and maps each match to an instant with instant().
func (e Expr) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	a := after.In(loc)
	w := time.Date(a.Year(), a.Month(), a.Day(), a.Hour(), a.Minute(), 0, 0, time.UTC).Add(time.Minute)
	end := w.AddDate(searchYears, 0, 0)
	for w.Before(end) {
		switch {
		case !has(e.month, int(w.Month())):
			w = time.Date(w.Year(), w.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !e.dayMatches(w):
			w = time.Date(w.Year(), w.Month(), w.Day()+1, 0, 0, 0, 0, time.UTC)
		case !has(e.hour, w.Hour()):
			w = w.Truncate(time.Hour).Add(time.Hour)
		case !has(e.minute, w.Minute()):
			w = w.Add(time.Minute)
		default:
			if t := instant(w, loc); t.After(after) {
				return t
			}
			w = w.Add(time.Minute)
		}
	}
	return time.Time{}
}

// NextN returns up to n successive occurrences after `after`.
func (e Expr) NextN(after time.Time, loc *time.Location, n int) []time.Time {
	out := make([]time.Time, 0, n)
	t := after
	for len(out) < n {
		t = e.Next(t, loc)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}

// Between counts the occurrences in [from, to] (up to max) and returns the
// latest one counted.
func (e Expr) Between(from, to time.Time, loc *time.Location, max int) (count int, latest time.Time) {
	t := from.Add(-time.Nanosecond)
	for count < max {
		t = e.Next(t, loc)
		if t.IsZero() || t.After(to) {
			break
		}
		count, latest = count+1, t
	}
	return count, latest
}

func (e Expr) dayMatches(w time.Time) bool {
	dom, dow := has(e.dom, w.Day()), has(e.dow, int(w.Weekday()))
	switch {
	case e.domStar && e.dowStar:
		return true
	case e.domStar:
		return dow
	case e.dowStar:
		return dom
	default:
		return dom || dow
	}
}

// instant maps a wall-clock minute w (in UTC fields) to the instant it
// names in loc. A wall time inside a spring-forward gap runs at the first
// valid instant after the gap. time.Date normalizes a gap wall time into a
// real instant in one of the two zone periods adjoining the gap, and which
// one depends on the zone and the direction of the shift: east of UTC (and
// in most zones) it lands after the gap, so ZoneBounds' start is the
// answer; west of UTC it can normalize backwards instead, landing before
// the gap, in which case the answer is that period's end (the same
// transition instant, viewed from the other side). The direction is
// detected by comparing the wall clock time.Date actually produced against
// the one that was requested, not by the zone's sign, because the rule
// that matters is which way time.Date moved, not where the zone sits.
// A wall time repeated by a fall-back resolves to its first (earlier)
// instant; time.Date's own choice between the two is unspecified.
func instant(w time.Time, loc *time.Location) time.Time {
	t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, loc)
	if !sameWall(t, w) {
		start, end := t.ZoneBounds()
		got := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
		if got.Before(w) {
			// time.Date normalized backwards, into the period before the
			// gap: the first valid instant is that period's end.
			return end
		}
		// time.Date normalized forwards, into the period after the gap:
		// its start is the first valid instant.
		return start
	}
	first := t
	// The back-off probes cover every DST shift in use today: 30 minutes
	// (e.g. Lord Howe) and 60 minutes, with a spare 2-hour probe. A
	// hypothetical zone repeating a wall-clock reading across a larger
	// offset change than that would resolve to a later instant instead of
	// the first, which this package cannot detect.
	for _, back := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour} {
		if u := t.Add(-back); sameWall(u, w) {
			first = u
		}
	}
	return first
}

func sameWall(t, w time.Time) bool {
	return t.Year() == w.Year() && t.Month() == w.Month() && t.Day() == w.Day() &&
		t.Hour() == w.Hour() && t.Minute() == w.Minute()
}

// Lookback returns the most recent occurrence at or before to, searching
// no further back than within. It exists because Between stops counting
// at its cap and therefore reports a `latest` that is the cap-th
// occurrence rather than the newest one (backlog 2.60); a caller that
// only needs to know how recent the newest occurrence is can bound the
// question instead, and this answers it in one forward scan over that
// bound. The zero time means there is no occurrence in the window.
func (e Expr) Lookback(from, to time.Time, loc *time.Location, within time.Duration) time.Time {
	start := to.Add(-within)
	if start.Before(from) {
		start = from
	}
	t := start.Add(-time.Nanosecond)
	var latest time.Time
	for {
		t = e.Next(t, loc)
		if t.IsZero() || t.After(to) {
			return latest
		}
		latest = t
	}
}
