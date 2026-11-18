package rules_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// TestVanished pins the rule itself (backlog 3.88): a reading has vanished
// when the newest reading of its own item is more than three of the
// check's intervals later — strictly more, so exactly three is still
// current. The clock plays no part: a whole item that went quiet together
// has no newer reading to trail, so it stays stale (and unknown), not
// vanished.
func TestVanished(t *testing.T) {
	newest := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		at       time.Time
		interval time.Duration
		want     bool
	}{
		{"the item's newest reading", newest, time.Minute, false},
		{"just inside three intervals", newest.Add(-3*time.Minute + time.Microsecond), time.Minute, false},
		{"exactly three intervals", newest.Add(-3 * time.Minute), time.Minute, false},
		{"just past three intervals", newest.Add(-3*time.Minute - time.Microsecond), time.Minute, true},
		{"far past", newest.Add(-time.Hour), time.Minute, true},
		{"measured against its own interval", newest.Add(-10 * time.Minute), 5 * time.Minute, false},
		{"newer than the newest (clock skew between rows)", newest.Add(time.Minute), time.Minute, false},
		{"no interval: nothing to measure against", newest.Add(-time.Hour), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Vanished(tc.at, newest, tc.interval); got != tc.want {
				t.Fatalf("Vanished(%v, %v, %v) = %v, want %v", tc.at, newest, tc.interval, got, tc.want)
			}
		})
	}
}

// TestItemNewestVanished: the newest reading is taken per (check key,
// item), over every reading of it — an error row included, since a failed
// collection is still the check running — so a fresh error is never
// vanished and keeps its item's other metrics from vanishing too, while a
// sibling item or another check key has no say.
func TestItemNewestVanished(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	// An error row is a reading like any other here: the rule reads only
	// when a row was written, never what it carries.
	type reading struct {
		tuple rules.Tuple
		at    time.Time
	}
	tu := func(key, item, metric string) rules.Tuple {
		return rules.Tuple{CheckKey: key, Item: item, Metric: metric}
	}
	for _, tc := range []struct {
		name     string
		readings []reading
		judge    rules.Tuple
		want     bool
	}{
		{
			name: "a timing metric a down probe no longer reports",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now},
				{tuple: tu("web", "", "http.dns_ms"), at: now.Add(-10 * time.Minute)},
			},
			judge: tu("web", "", "http.dns_ms"), want: true,
		},
		{
			name: "the fresh sibling itself",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now},
				{tuple: tu("web", "", "http.dns_ms"), at: now.Add(-10 * time.Minute)},
			},
			judge: tu("web", "", "http.up"), want: false,
		},
		{
			name: "a fresh error is never vanished",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now.Add(-10 * time.Minute)},
				{tuple: tu("web", "", "http.failure"), at: now},
			},
			judge: tu("web", "", "http.failure"), want: false,
		},
		{
			name: "a fresh error counts as the item's newest reading",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now.Add(-10 * time.Minute)},
				{tuple: tu("web", "", "http.failure"), at: now},
			},
			judge: tu("web", "", "http.up"), want: true,
		},
		{
			name: "an error just inside three intervals of the newest",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now},
				{tuple: tu("web", "", "http.failure"), at: now.Add(-2 * time.Minute)},
			},
			judge: tu("web", "", "http.failure"), want: false,
		},
		{
			name: "a whole item gone quiet together stays stale, not vanished",
			readings: []reading{
				{tuple: tu("disk", "/data", "disk.used_pct"), at: now.Add(-time.Hour)},
				{tuple: tu("disk", "/data", "disk.free_bytes"), at: now.Add(-time.Hour)},
				{tuple: tu("disk", "/", "disk.used_pct"), at: now},
			},
			judge: tu("disk", "/data", "disk.used_pct"), want: false,
		},
		{
			name: "another check key's fresher reading has no say",
			readings: []reading{
				{tuple: tu("cpu", "", "cpu.used_pct"), at: now},
				{tuple: tu("web", "", "http.dns_ms"), at: now.Add(-time.Hour)},
			},
			judge: tu("web", "", "http.dns_ms"), want: false,
		},
		{
			name: "a reading of an item never observed is its own newest",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now},
			},
			judge: tu("web", "status", "http.json.value"), want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newest := rules.ItemNewest{}
			var at time.Time
			for _, r := range tc.readings {
				newest.Observe(r.tuple, r.at)
				if r.tuple == tc.judge {
					at = r.at
				}
			}
			if at.IsZero() {
				at = now
			}
			if got := newest.Vanished(tc.judge, at, time.Minute); got != tc.want {
				t.Fatalf("Vanished(%+v) = %v, want %v", tc.judge, got, tc.want)
			}
		})
	}
}
