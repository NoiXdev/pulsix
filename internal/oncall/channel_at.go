package oncall

import (
	"bytes"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// handoverIndex counts how many handovers have happened at or before at,
// relative to the anchor. It may be negative: a rotation created today
// for a cycle anchored next Monday is an ordinary thing to configure, and
// every instant before that anchor sits at a negative index.
//
// The counting is CALENDAR arithmetic, stepping whole days in the
// rotation's own location, and not (at - anchor) / period. Duration
// arithmetic drifts: with a seven-day period and a handover at 09:00
// local, every daylight-saving change moves that handover an hour and the
// error accumulates, until years later the rota hands over at night.
func handoverIndex(r Rotation, at time.Time) (int, error) {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return 0, fmt.Errorf("rotation %s: unknown timezone %q: %w", r.ID, r.Timezone, err)
	}
	return handoverIndexAt(r, loc, at), nil
}

// handoverIndexAt is handoverIndex's arithmetic core against an
// already-resolved location. It exists so a caller that needs many lookups
// against the same rotation — Shifts, walking a whole range segment by
// segment — pays for time.LoadLocation once instead of once per lookup:
// Go only caches UTC and Local, so every other zone re-reads zoneinfo
// (tens of microseconds) on every call.
func handoverIndexAt(r Rotation, loc *time.Location, at time.Time) int {
	anchor := r.Anchor.In(loc)
	target := at.In(loc)

	// Seed the search with the whole calendar days between the two dates,
	// then correct. The count is taken from the dates themselves, never
	// from target.Sub(anchor): a Duration saturates at ±292 years, so for
	// an anchor further out than that the seed stops tracking the real
	// distance and the correction loop below walks the remainder one
	// period at a time — millions of AddDate calls, seconds of CPU, for a
	// roster the GUI asks fourteen days of (whole-branch review,
	// Critical #1). daysSinceCivilEpoch is exact for every instant
	// time.Time can hold, so the loops only ever correct the rounding and
	// the time of day: at most a couple of steps, whatever the anchor.
	days := daysSinceCivilEpoch(target) - daysSinceCivilEpoch(anchor)
	guess := days / r.PeriodDays
	// AddDate normalizes a date that does not exist locally (02:30 on a
	// spring-forward day), which is the defined behaviour this relies on.
	for anchor.AddDate(0, 0, guess*r.PeriodDays).After(target) {
		guess--
	}
	for !anchor.AddDate(0, 0, (guess+1)*r.PeriodDays).After(target) {
		guess++
	}
	return guess
}

// ChannelAt reports which channel is on call at at: the cycle's answer,
// overridden by the winning stand-in when one covers that instant.
func ChannelAt(r Rotation, exc []Exception, at time.Time) (uuid.UUID, error) {
	if len(r.Channels) == 0 {
		return uuid.Nil, fmt.Errorf("%w: rotation %s has no channels", ErrInvalidInput, r.ID)
	}
	// handoverIndex divides by PeriodDays and its loop direction assumes it
	// is positive. Zero panics (integer divide by zero); negative reverses
	// the loop's monotonicity and never terminates — a wedged goroutine,
	// with no error and no timeout, inside a function a ticker calls.
	// Validate and the column's CHECK both block this today, but that is
	// the same pair of layers that already covers the channel-count
	// invariant above, and defending one half of that pair and not the
	// other is an asymmetry whose skipped half fails worse.
	if r.PeriodDays <= 0 {
		return uuid.Nil, fmt.Errorf("%w: rotation %s has an invalid period %d", ErrInvalidInput, r.ID, r.PeriodDays)
	}
	if win, ok := winningException(exc, at); ok {
		return win.ChannelID, nil
	}
	idx, err := handoverIndex(r, at)
	if err != nil {
		return uuid.Nil, err
	}
	n := len(r.Channels)
	// Go's % keeps the sign of the dividend, so a negative index would
	// address outside the slice. The second modulo folds it back.
	return r.Channels[((idx%n)+n)%n], nil
}

// channelAndStandInAt mirrors ChannelAt's decision — a winning stand-in
// overrides the cycle — against an already-resolved location, and also
// reports whether the answer came from a stand-in. The caller must have
// already checked len(r.Channels) and r.PeriodDays, exactly as ChannelAt
// does, since this skips both to stay a cheap per-segment call for Shifts.
//
// StandIn comes from asking winningException directly rather than from
// comparing the result against the cycle's own answer: a stand-in may
// legitimately name the same channel the cycle would have picked, and a
// comparison would then report ordinary cycle time as a stand-in.
func channelAndStandInAt(r Rotation, exc []Exception, loc *time.Location, at time.Time) (uuid.UUID, bool) {
	if win, ok := winningException(exc, at); ok {
		return win.ChannelID, true
	}
	idx := handoverIndexAt(r, loc, at)
	n := len(r.Channels)
	return r.Channels[((idx%n)+n)%n], false
}

// winningException picks the stand-in covering at under the strict total
// order of decision 7: later start wins; same start, later end wins; a
// tie goes to the smaller id, byte for byte. Ids are unique, so no two
// distinct exceptions ever compare equal and folding over a slice never
// depends on the order it was visited in.
//
// The span is half-open, [StartsAt, EndsAt): a stand-in that ends at 09:00
// is over at 09:00, which is what lets two consecutive stand-ins meet
// without overlapping.
func winningException(exc []Exception, at time.Time) (Exception, bool) {
	var best Exception
	found := false
	for _, e := range exc {
		if at.Before(e.StartsAt) || !at.Before(e.EndsAt) {
			continue
		}
		if !found || beatsException(e, best) {
			best, found = e, true
		}
	}
	return best, found
}

func beatsException(a, b Exception) bool {
	if !a.StartsAt.Equal(b.StartsAt) {
		return a.StartsAt.After(b.StartsAt)
	}
	if !a.EndsAt.Equal(b.EndsAt) {
		return a.EndsAt.After(b.EndsAt)
	}
	return bytes.Compare(a.ID[:], b.ID[:]) < 0
}

// daysSinceCivilEpoch counts whole days from 1970-01-01 to t's own calendar
// date, read in whatever location t already carries. It is Howard
// Hinnant's days_from_civil, the standard closed form for the proleptic
// Gregorian calendar Go's own time package uses, and it exists so
// handoverIndexAt can measure the distance between two dates without going
// through a Duration — the one arithmetic in this file that cannot
// represent the distances a stored anchor may actually sit at.
//
// The time of day is deliberately dropped: the caller wants a whole-day
// count to divide by a whole-day period, and its correction loop is what
// settles which side of a handover an instant falls on.
func daysSinceCivilEpoch(t time.Time) int {
	year, month, day := t.Date()
	y, m, d := int64(year), int64(month), int64(day)
	// March-based years, so a leap day lands at the end and needs no case
	// of its own.
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400 // floor division, not truncation toward zero
	}
	yoe := y - era*400 // [0, 399]
	mp := m - 3        // March is 0
	if m <= 2 {
		mp = m + 9
	}
	doy := (153*mp+2)/5 + d - 1            // [0, 365]
	doe := yoe*365 + yoe/4 - yoe/100 + doy // [0, 146096]
	return int(era*146097 + doe - 719468)
}
