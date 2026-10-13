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

	// Step forward or backward a period at a time. The loop is bounded by
	// the distance in days divided by the period, so it is linear in the
	// number of periods rather than in time.
	days := int(target.Sub(anchor).Hours() / 24)
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
		return uuid.Nil, fmt.Errorf("rotation %s has no channels", r.ID)
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
		return uuid.Nil, fmt.Errorf("rotation %s has an invalid period %d", r.ID, r.PeriodDays)
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
