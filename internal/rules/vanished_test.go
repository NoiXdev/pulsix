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
// item), over its error-free readings only — a value or a label proves the
// check still reports the item's metrics, an error proves only that the
// collection ran (a SMART disk smartctl cannot open reports one error on
// smart.passed and nothing else) — while an error row is itself judged
// against that newest like any other row. A sibling item or another check
// key has no say.
func TestItemNewestVanished(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	type reading struct {
		tuple rules.Tuple
		at    time.Time
		err   string
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
				{tuple: tu("web", "", "http.failure"), at: now, err: "connection refused"},
			},
			judge: tu("web", "", "http.failure"), want: false,
		},
		{
			name: "a fresh error does not count as the item's newest reading",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now.Add(-10 * time.Minute)},
				{tuple: tu("web", "", "http.failure"), at: now, err: "connection refused"},
			},
			judge: tu("web", "", "http.up"), want: false,
		},
		{
			name: "a disk smartctl cannot open keeps its other metrics",
			readings: []reading{
				{tuple: tu("smart", "/dev/sda", "smart.passed"), at: now, err: "smartctl: cannot open device"},
				{tuple: tu("smart", "/dev/sda", "smart.reallocated_sectors"), at: now.Add(-time.Hour)},
				{tuple: tu("smart", "/dev/sda", "smart.temperature_celsius"), at: now.Add(-time.Hour)},
			},
			judge: tu("smart", "/dev/sda", "smart.reallocated_sectors"), want: false,
		},
		{
			name: "an error-free sibling still vanishes an old error row",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now},
				{tuple: tu("web", "", "http.dns_ms"), at: now.Add(-10 * time.Minute), err: "no such host"},
			},
			judge: tu("web", "", "http.dns_ms"), want: true,
		},
		{
			name: "an error just inside three intervals of the newest",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now},
				{tuple: tu("web", "", "http.failure"), at: now.Add(-2 * time.Minute), err: "connection refused"},
			},
			judge: tu("web", "", "http.failure"), want: false,
		},
		{
			name: "an item with only errors has no newest: nothing vanishes",
			readings: []reading{
				{tuple: tu("web", "", "http.up"), at: now, err: "secret unavailable"},
				{tuple: tu("web", "", "http.dns_ms"), at: now.Add(-time.Hour), err: "secret unavailable"},
			},
			judge: tu("web", "", "http.dns_ms"), want: false,
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
				newest.Observe(r.tuple, r.at, r.err)
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

// TestItemNewestReplaying (backlog 3.118): an item is replaying while its
// newest error-free reading predates the start of the agent's current
// online period — every reading of it the manager holds was collected
// before the agent came back, so the rest of that collection may still be
// in flight in a later chunk of the replay. Its own first reading of the
// period ends it. A host without a known online period (no agent, or an
// unknown start) never replays, and an item with nothing but errors has no
// newest reading to judge.
func TestItemNewestReplaying(t *testing.T) {
	since := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	up := rules.Tuple{CheckKey: "web", Metric: "http.up"}
	dns := rules.Tuple{CheckKey: "web", Metric: "http.dns_ms"}
	disk := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	for _, tc := range []struct {
		name   string
		newest time.Time
		errs   bool
		since  time.Time
		judge  rules.Tuple
		want   bool
	}{
		{name: "newest reading collected before the agent came back", newest: since.Add(-15 * time.Minute), since: since, judge: dns, want: true},
		{name: "a sibling metric shares its item's verdict", newest: since.Add(-15 * time.Minute), since: since, judge: up, want: true},
		{name: "a reading taken as the period began ends it", newest: since, since: since, judge: dns, want: false},
		{name: "a live reading of the period ends it", newest: since.Add(time.Second), since: since, judge: dns, want: false},
		{name: "no known online period", newest: since.Add(-15 * time.Minute), judge: dns, want: false},
		{name: "an item with nothing but errors", newest: since.Add(-15 * time.Minute), errs: true, since: since, judge: dns, want: false},
		{name: "another item's readings have no say", newest: since.Add(-15 * time.Minute), since: since, judge: disk, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := rules.ItemNewest{}
			errText := ""
			if tc.errs {
				errText = "connection refused"
			}
			n.Observe(up, tc.newest, errText)
			n.Observe(dns, tc.newest.Add(-30*time.Minute), errText)
			if got := n.Replaying(tc.judge, tc.since); got != tc.want {
				t.Fatalf("Replaying(%+v, %v) = %v, want %v", tc.judge, tc.since, got, tc.want)
			}
		})
	}
}
