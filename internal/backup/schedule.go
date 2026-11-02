package backup

import (
	"fmt"
	"time"
)

// SlotFor is the scheduled slot a daily backup at timeOfDay ("HH:MM", local
// to loc; nil means UTC) takes at now: the most recent local occurrence of
// timeOfDay at or before now.
//
// Every local day has exactly one slot, across daylight-saving switches
// too. On the day the clocks skip timeOfDay, the slot is the first instant
// after the gap (02:30 in Europe/Berlin on the spring-forward day becomes
// 03:00 CEST); on the day timeOfDay occurs twice, the first occurrence is
// the slot. An unparsable timeOfDay is treated as 00:00; SaveSettings never
// stores one.
func SlotFor(now time.Time, timeOfDay string, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	h, m, _ := parseTimeOfDay(timeOfDay)
	y, mo, d := now.In(loc).Date()
	// Today's slot, or yesterday's when today's is still ahead. Two days back
	// covers any offset change a real zone makes in a day.
	for back := 0; back < 3; back++ {
		if slot := slotOn(y, mo, d-back, h, m, loc); !slot.After(now) {
			return slot
		}
	}
	return slotOn(y, mo, d-3, h, m, loc) // unreachable for real zones
}

// nextSlotAfter is the first slot strictly after t.
func nextSlotAfter(t time.Time, timeOfDay string, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	h, m, _ := parseTimeOfDay(timeOfDay)
	y, mo, d := t.In(loc).Date()
	for ahead := 0; ; ahead++ {
		if slot := slotOn(y, mo, d+ahead, h, m, loc); slot.After(t) {
			return slot
		}
	}
}

// slotOn is the instant of h:m on the local day y-mo-d (normalized, so d may
// be out of range), resolving a daylight-saving gap or overlap as SlotFor
// documents.
//
// time.Date leaves the choice in a gap or an overlap unspecified, so the
// wall clock is resolved by hand: it is tried with the offsets in force well
// before and well after the day (a real zone changes its offset at most once
// a day), and the earliest candidate whose local clock really reads h:m
// wins. When neither does, the clock skipped h:m, and the slot is the
// moment the new offset took effect.
func slotOn(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	wall := time.Date(y, mo, d, h, m, 0, 0, time.UTC)
	before := offsetAt(wall.Add(-36*time.Hour), loc)
	after := offsetAt(wall.Add(36*time.Hour), loc)

	var best time.Time
	for _, off := range []int{before, after} {
		at := wall.Add(-time.Duration(off) * time.Second)
		local := at.In(loc)
		if local.Hour() != h || local.Minute() != m || local.Day() != wall.Day() {
			continue
		}
		if best.IsZero() || at.Before(best) {
			best = at
		}
	}
	if !best.IsZero() {
		return best
	}
	// A gap: read with the old offset, the wall clock lands after the
	// switch; the zone period it lands in starts at the switch.
	start, _ := wall.Add(-time.Duration(before) * time.Second).In(loc).ZoneBounds()
	return start
}

// offsetAt is loc's UTC offset in seconds at the instant t.
func offsetAt(t time.Time, loc *time.Location) int {
	_, off := t.In(loc).Zone()
	return off
}

// parseTimeOfDay parses "HH:MM" on the 24-hour clock.
func parseTimeOfDay(s string) (h, m int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != len("15:04") {
		return 0, 0, fmt.Errorf("time of day %q is not HH:MM", s)
	}
	return t.Hour(), t.Minute(), nil
}
