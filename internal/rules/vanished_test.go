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

// TestHostReplaying (backlog 3.118): a host is replaying from the start of
// its agent's online period until the first reading of a check the agent
// runs that was taken at or after that start — the agent sends nothing
// live before its replay is through — and never for longer than the cap,
// so a host whose agent reports nothing live (or whose clock runs behind)
// is judged normally again. A host without a known online period never
// replays.
func TestHostReplaying(t *testing.T) {
	since := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cap := rules.ReplayHoldCap
	for _, tc := range []struct {
		name         string
		since, agent time.Time
		now          time.Time
		want         bool
	}{
		{name: "only readings from before the period", since: since, agent: since.Add(-15 * time.Minute), now: since.Add(5 * time.Second), want: true},
		{name: "no agent reading at all", since: since, now: since.Add(5 * time.Second), want: true},
		{name: "a reading taken as the period began ends it", since: since, agent: since, now: since.Add(5 * time.Second), want: false},
		{name: "a live reading ends it", since: since, agent: since.Add(time.Second), now: since.Add(5 * time.Second), want: false},
		{name: "just inside the cap", since: since, agent: since.Add(-time.Minute), now: since.Add(cap - time.Microsecond), want: true},
		{name: "the cap ends it", since: since, agent: since.Add(-time.Minute), now: since.Add(cap), want: false},
		{name: "no known online period", agent: since.Add(-15 * time.Minute), now: since, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.HostReplaying(tc.since, tc.agent, tc.now); got != tc.want {
				t.Fatalf("HostReplaying(%v, %v, %v) = %v, want %v", tc.since, tc.agent, tc.now, got, tc.want)
			}
		})
	}
}
