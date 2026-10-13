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
	return guess, nil
}

// ChannelAt reports which channel is on call at at: the cycle's answer,
// overridden by the winning stand-in when one covers that instant.
func ChannelAt(r Rotation, exc []Exception, at time.Time) (uuid.UUID, error) {
	if len(r.Channels) == 0 {
		return uuid.Nil, fmt.Errorf("rotation %s has no channels", r.ID)
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
