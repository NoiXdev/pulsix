package backup_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

// slotsPerDay evaluates SlotFor for every hour (and every half hour, so a
// slot at hh:30 is approached from both sides) between from and to, and
// returns the distinct slots grouped by their local day.
func slotsPerDay(t *testing.T, from, to time.Time, timeOfDay string, loc *time.Location) map[string][]time.Time {
	t.Helper()
	seen := make(map[time.Time]bool)
	days := make(map[string][]time.Time)
	for now := from; !now.After(to); now = now.Add(30 * time.Minute) {
		slot := backup.SlotFor(now, timeOfDay, loc)
		if slot.After(now) {
			t.Fatalf("SlotFor(%s) = %s, after now", now, slot)
		}
		if now.Sub(slot) > 25*time.Hour {
			t.Fatalf("SlotFor(%s) = %s, more than a day back", now, slot)
		}
		if seen[slot] {
			continue
		}
		seen[slot] = true
		day := slot.In(loc).Format("2006-01-02")
		days[day] = append(days[day], slot)
	}
	return days
}

func TestSlotForDailyBerlinAcrossDST(t *testing.T) {
	loc := berlin(t)
	for _, window := range []struct{ from, to string }{
		{"2026-03-27", "2026-04-01"}, // spring forward: 02:00 CET -> 03:00 CEST on 03-29
		{"2026-10-23", "2026-10-28"}, // fall back: 03:00 CEST -> 02:00 CET on 10-25
	} {
		from, _ := time.ParseInLocation("2006-01-02", window.from, loc)
		to, _ := time.ParseInLocation("2006-01-02", window.to, loc)
		for _, tod := range []string{"03:00", "02:30"} {
			days := slotsPerDay(t, from, to, tod, loc)
			// Every local day from from up to (not including) to has
			// exactly one slot: none skipped, none doubled.
			for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
				day := d.Format("2006-01-02")
				if n := len(days[day]); n != 1 {
					t.Errorf("%s %s: %d slots on %s (%v), want exactly one", window.from, tod, n, day, days[day])
				}
			}
		}
	}

	spring := backup.SlotFor(time.Date(2026, 3, 29, 12, 0, 0, 0, loc), "02:30", loc)
	if want := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC); !spring.Equal(want) {
		t.Errorf("02:30 on the spring-forward day = %s, want 03:00 CEST (%s)", spring.In(loc), want.In(loc))
	}
	autumn := backup.SlotFor(time.Date(2026, 10, 25, 12, 0, 0, 0, loc), "02:30", loc)
	if want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC); !autumn.Equal(want) {
		t.Errorf("02:30 on the fall-back day = %s, want the first occurrence, 02:30 CEST (%s)", autumn.In(loc), want.In(loc))
	}
}

func TestSlotFor(t *testing.T) {
	loc := berlin(t)
	for _, tc := range []struct {
		name      string
		now       time.Time
		timeOfDay string
		loc       *time.Location
		want      time.Time
	}{
		{"after the time today", time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), "03:00", time.UTC, time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)},
		{"exactly at the time", time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC), "03:00", time.UTC, time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)},
		{"before the time today", time.Date(2026, 11, 2, 2, 59, 59, 0, time.UTC), "03:00", time.UTC, time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)},
		{"local day differs from UTC day", time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC), "00:15", loc, time.Date(2026, 11, 1, 23, 15, 0, 0, time.UTC)},
		{"nil location is UTC", time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), "23:45", nil, time.Date(2026, 11, 1, 23, 45, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backup.SlotFor(tc.now, tc.timeOfDay, tc.loc); !got.Equal(tc.want) {
				t.Errorf("SlotFor = %s, want %s", got, tc.want)
			}
		})
	}
}
