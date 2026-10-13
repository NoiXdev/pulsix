package oncall

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// MaxRange bounds what one Shifts call may be asked for. Without it the
// range is whatever a query string says: from=0001-01-01 to=9999-12-31 with
// a one-day period walks roughly 3.6 million handovers, sorts them, and
// builds as many segments — about a gigabyte and tens of seconds for one
// request, from anyone holding channels.read, inside the process that is
// also the alert follower. internal/calendar.MaxRange is the precedent;
// this is the same guard on this package's own range-taking verb.
//
// A year is far more than any caller needs — the roster strip asks for
// fourteen days (web/src/features/notifications/RotationsPage.tsx) — and is
// the longest span over which a rotation's answer is worth drawing at all.
// It also bounds the work exactly: the shortest period this package accepts
// is one day, so a year's range yields at most 366 handovers and, with the
// stand-ins covering it, a few hundred segments.
const MaxRange = 366 * 24 * time.Hour

// Shift is one segment of the roster: from its start up to (not including)
// its end, this channel is on call. StandIn marks a segment that came from
// an Exception rather than the cycle itself.
type Shift struct {
	StartsAt, EndsAt time.Time
	ChannelID        uuid.UUID
	StandIn          bool
}

// Shifts answers who is on call over [from, to), split into segments that
// tile the range exactly: the first starts at from, the last ends at to,
// and each segment's start equals the previous one's end. No two adjacent
// segments ever name the same channel with the same StandIn flag — a
// handover between two turns of a one-channel rotation, or a stand-in that
// echoes the channel the cycle would have picked anyway, is merged into
// its neighbour rather than reported as a boundary nothing actually
// crossed.
func Shifts(r Rotation, exc []Exception, from, to time.Time) ([]Shift, error) {
	if !from.Before(to) {
		return nil, fmt.Errorf("%w: a shift range must start before it ends", ErrInvalidInput)
	}
	// to.Sub(from) saturates at the largest representable Duration rather
	// than wrapping, so the widest range RFC 3339 can express compares
	// above MaxRange and is refused here like any other overlong one.
	if to.Sub(from) > MaxRange {
		return nil, fmt.Errorf("%w: a shift range may cover at most %s, got %s", ErrInvalidInput, MaxRange, to.Sub(from))
	}
	// Same guards ChannelAt applies, repeated here because the per-segment
	// loop below calls channelAndStandInAt directly rather than ChannelAt,
	// and so does not get them for free.
	if len(r.Channels) == 0 {
		return nil, fmt.Errorf("rotation %s has no channels", r.ID)
	}
	if r.PeriodDays <= 0 {
		return nil, fmt.Errorf("rotation %s has an invalid period %d", r.ID, r.PeriodDays)
	}

	// Resolve the rotation's location once. handoverIndex and ChannelAt each
	// call time.LoadLocation on every invocation; Go only caches UTC and
	// Local, so any other zone re-reads zoneinfo (tens of microseconds)
	// every time. Over a month-long roster split into many segments that
	// cost is paid once per segment instead of once total, so the boundary
	// search and the per-segment lookups below go through the location-
	// accepting cores (handoverIndexAt, channelAndStandInAt) against this
	// one resolved value instead of calling handoverIndex/ChannelAt in a
	// loop.
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return nil, fmt.Errorf("rotation %s: unknown timezone %q: %w", r.ID, r.Timezone, err)
	}

	bounds := boundaries(r, loc, exc, from, to)

	segments := make([]Shift, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		start, end := bounds[i], bounds[i+1]
		channel, standIn := channelAndStandInAt(r, exc, loc, start)

		if last := len(segments) - 1; last >= 0 &&
			segments[last].ChannelID == channel && segments[last].StandIn == standIn {
			segments[last].EndsAt = end
			continue
		}
		segments = append(segments, Shift{
			StartsAt:  start,
			EndsAt:    end,
			ChannelID: channel,
			StandIn:   standIn,
		})
	}
	return segments, nil
}

// boundaries collects every instant inside (from, to) where the answer
// might change — handovers and exception edges — plus from and to
// themselves, sorted and deduplicated so walking consecutive pairs yields
// the candidate segments.
func boundaries(r Rotation, loc *time.Location, exc []Exception, from, to time.Time) []time.Time {
	startIdx := handoverIndexAt(r, loc, from)
	endIdx := handoverIndexAt(r, loc, to)

	anchor := r.Anchor.In(loc)
	bounds := []time.Time{from, to}
	for idx := startIdx + 1; idx <= endIdx; idx++ {
		h := anchor.AddDate(0, 0, idx*r.PeriodDays)
		if h.After(from) && h.Before(to) {
			bounds = append(bounds, h)
		}
	}
	for _, e := range exc {
		if e.StartsAt.After(from) && e.StartsAt.Before(to) {
			bounds = append(bounds, e.StartsAt)
		}
		if e.EndsAt.After(from) && e.EndsAt.Before(to) {
			bounds = append(bounds, e.EndsAt)
		}
	}

	sort.Slice(bounds, func(i, j int) bool { return bounds[i].Before(bounds[j]) })
	return dedupe(bounds)
}

// dedupe collapses consecutive equal instants in an already-sorted slice.
func dedupe(ts []time.Time) []time.Time {
	out := ts[:0:0]
	for i, t := range ts {
		if i == 0 || !t.Equal(ts[i-1]) {
			out = append(out, t)
		}
	}
	return out
}
