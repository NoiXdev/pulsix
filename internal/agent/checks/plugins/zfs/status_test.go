package zfs

import (
	"math"
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.DateTime, s)
	if err != nil {
		panic(err)
	}
	return t
}

// wantPool is one pool's expected status-derived values. lastScrub zero
// means no finished scrub is named (the age counts from creation).
type wantPool struct {
	kind                      string
	pct                       wantNum
	read, write, cksum        wantNum
	unhealthy, dataErrors     wantNum
	lastScrub                 time.Time
	lastScrubErr, kindErrWant bool
}

func checkPool(t *testing.T, name string, got poolStatus, w wantPool) {
	t.Helper()
	if w.kindErrWant {
		if got.scan.kindErr == nil {
			t.Errorf("%s scan kind = %q, want an error", name, got.scan.kind)
		}
	} else if got.scan.kindErr != nil || got.scan.kind != w.kind {
		t.Errorf("%s scan kind = %q (%v), want %q", name, got.scan.kind, got.scan.kindErr, w.kind)
	}
	checkNum(t, name+" scan_pct", got.scan.pct, w.pct)
	checkNum(t, name+" errors_read", got.readErrors, w.read)
	checkNum(t, name+" errors_write", got.writeErrors, w.write)
	checkNum(t, name+" errors_checksum", got.checksumErrors, w.cksum)
	checkNum(t, name+" devices_unhealthy", got.devicesUnhealthy, w.unhealthy)
	checkNum(t, name+" data_errors", got.dataErrors, w.dataErrors)
	if w.lastScrubErr {
		if got.scan.lastScrubErr == nil {
			t.Errorf("%s last scrub = %v, want an error", name, got.scan.lastScrub)
		}
		return
	}
	if got.scan.lastScrubErr != nil || !got.scan.lastScrub.Equal(w.lastScrub) {
		t.Errorf("%s last scrub = %v (%v), want %v", name, got.scan.lastScrub, got.scan.lastScrubErr, w.lastScrub)
	}
}

func TestParseStatus(t *testing.T) {
	zero := num(0)
	for _, tc := range []struct {
		file string
		want map[string]wantPool
	}{
		{"status_mirror_healthy.txt", map[string]wantPool{
			"tank": {kind: "none", pct: num(100), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero,
				lastScrub: utc("2024-11-10 00:36:35")},
		}},
		// A FAULTED leaf in a DEGRADED raidz2; a single-digit day padded
		// with a space, as ctime prints it.
		{"status_raidz2_degraded.txt", map[string]wantPool{
			"data": {kind: "none", pct: num(100), read: num(3), write: num(87), cksum: zero, unhealthy: num(1), dataErrors: zero,
				lastScrub: utc("2024-11-03 05:36:46")},
		}},
		// I/O suspended: the errors line names no count.
		{"status_faulted.txt", map[string]wantPool{
			"backup": {kind: "none", pct: num(100), read: num(3), write: num(12), cksum: zero, unhealthy: num(1), dataErrors: numErr,
				lastScrub: utc("2024-10-13 01:26:04")},
		}},
		// The spare stands in under spare-0 and is ONLINE there; the spares
		// group's INUSE and AVAIL rows count for nothing.
		{"status_resilver_spare.txt", map[string]wantPool{
			"tank": {kind: "resilver", pct: num(24.61), read: num(12), write: num(344), cksum: zero, unhealthy: num(1), dataErrors: zero},
		}},
		{"status_scrub_progress.txt", map[string]wantPool{
			"tank": {kind: "scrub", pct: num(12.30), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		{"status_scrub_progress_2_2.txt", map[string]wantPool{
			"tank": {kind: "scrub", pct: num(12.30), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		{"status_scrub_paused.txt", map[string]wantPool{
			"tank": {kind: "scrub_paused", pct: num(12.30), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		// A canceled scrub is not a finished one.
		{"status_scrub_canceled.txt", map[string]wantPool{
			"tank": {kind: "none", pct: num(100), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		{"status_never_scrubbed.txt", map[string]wantPool{
			"fresh": {kind: "none", pct: num(100), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		// A finished resilver is not a scrub.
		{"status_resilvered.txt", map[string]wantPool{
			"tank": {kind: "none", pct: num(100), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		// Every row counts its own errors: pool 0/0/1, raidz1-0 0/0/4, sda
		// 2/0/3, sdc 0/1/0, dedup's nvme1n1p3 0/0/5, special's nvme0n1p2
		// 1/0/0, the logs mirror 0/2/0, cache nvme2n1 7/0/0. The UNAVAIL
		// cache device is the one unhealthy leaf; the AVAIL spare is none.
		{"status_groups.txt", map[string]wantPool{
			"fast": {kind: "none", pct: num(100), read: num(10), write: num(3), cksum: num(13), unhealthy: num(1), dataErrors: zero,
				lastScrub: utc("2024-11-10 01:05:09")},
		}},
		{"status_data_errors.txt", map[string]wantPool{
			"tank": {kind: "none", pct: num(100), read: zero, write: zero, cksum: num(6), unhealthy: zero, dataErrors: num(3),
				lastScrub: utc("2024-11-10 00:44:13")},
		}},
		{"status_two_pools.txt", map[string]wantPool{
			"rpool": {kind: "none", pct: num(100), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero,
				lastScrub: utc("2024-11-10 00:25:53")},
			"tank": {kind: "none", pct: num(100), read: zero, write: zero, cksum: zero, unhealthy: zero, dataErrors: zero},
		}},
		// Older releases: "scanned out of" without a percentage (1.23T of
		// 3.62T, checked below), an old finished-scrub line, a counter
		// abbreviated as 1.2K (1.2 × 1024, rounded).
		{"status_old.txt", map[string]wantPool{
			"backup": {kind: "none", pct: num(100), read: num(1229), write: num(3), cksum: num(15), unhealthy: num(1), dataErrors: zero,
				lastScrub: utc("2024-11-10 00:36:35")},
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			got := parseStatus(load(t, tc.file), time.UTC)
			for name, w := range tc.want {
				g, ok := got[name]
				if !ok {
					t.Fatalf("pool %q missing: %+v", name, got)
				}
				checkPool(t, name, g, w)
			}
		})
	}
}

func TestParseStatusOldComputedPercentage(t *testing.T) {
	got := parseStatus(load(t, "status_old.txt"), time.UTC)
	p, ok := got["tank"]
	if !ok {
		t.Fatalf("tank missing: %+v", got)
	}
	want := 1.23 / 3.62 * 100
	if p.scan.kind != "scrub" || p.scan.pct.err != nil || math.Abs(p.scan.pct.value-want) > 1e-9 {
		t.Fatalf("scan = %+v, want scrub at %v", p.scan, want)
	}
}

func TestParseStatusNoPools(t *testing.T) {
	for _, in := range []string{"", "no pools available\n"} {
		if got := parseStatus([]byte(in), time.UTC); len(got) != 0 {
			t.Errorf("parseStatus(%q) = %+v", in, got)
		}
	}
}

// TestParseStatusPoolNameWithSpace: a pool name may hold a space; the name
// is the whole rest of the pool line, and the tree's rows still parse.
func TestParseStatusPoolNameWithSpace(t *testing.T) {
	in := "  pool: my pool\n state: ONLINE\n  scan: none requested\nconfig:\n\n" +
		"\tNAME        STATE     READ WRITE CKSUM\n" +
		"\tmy pool     ONLINE       0     0     2\n" +
		"\t  sda       ONLINE       1     0     0\n\n" +
		"errors: No known data errors\n"
	p, ok := parseStatus([]byte(in), time.UTC)["my pool"]
	if !ok {
		t.Fatal("pool missing")
	}
	checkPool(t, "my pool", p, wantPool{kind: "none", pct: num(100), read: num(1), write: num(0), cksum: num(2), unhealthy: num(0), dataErrors: num(0)})
}

// TestParseStatusMissingParts: a pool paragraph without a tree or an errors
// line reports those metrics as errors, not as zero.
func TestParseStatusMissingParts(t *testing.T) {
	in := "  pool: tank\n state: ONLINE\n  scan: none requested\n"
	p, ok := parseStatus([]byte(in), time.UTC)["tank"]
	if !ok {
		t.Fatal("pool missing")
	}
	checkPool(t, "tank", p, wantPool{kind: "none", pct: num(100), read: numErr, write: numErr, cksum: numErr, unhealthy: numErr, dataErrors: numErr})
}

// TestParseStatusBadCounter: a counter that does not parse is that sum's
// error; the other sums stand.
func TestParseStatusBadCounter(t *testing.T) {
	in := "  pool: tank\n state: ONLINE\nconfig:\n\n" +
		"\tNAME        STATE     READ WRITE CKSUM\n" +
		"\ttank        ONLINE       0     0     0\n" +
		"\t  sda       ONLINE       x     1     0\n\n" +
		"errors: 12 data errors, use '-v' for a list\n"
	p := parseStatus([]byte(in), time.UTC)["tank"]
	checkPool(t, "tank", p, wantPool{kind: "none", pct: num(100), read: numErr, write: num(1), cksum: num(0), unhealthy: num(0), dataErrors: num(12)})
}

func TestParseCounter(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0", 0, true},
		{"18446744073709551615", 18446744073709551615, true},
		{"1.2K", 1229, true},
		{"10K", 10240, true},
		{"3.5M", 3670016, true},
		{"1G", 1 << 30, true},
		{"1T", 1 << 40, true},
		{"-1", 0, false},
		{"x", 0, false},
		{"1.2Q", 0, false},
		{"", 0, false},
		{"K", 0, false},
	} {
		got, err := parseCounter(tc.in)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseCounter(%q) = %v, %v; want %v ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestParseScan(t *testing.T) {
	end := utc("2024-11-10 00:36:35")
	for _, tc := range []struct {
		desc      string
		lines     string
		kind      string
		kindErr   bool
		pct       wantNum
		lastScrub time.Time
		endErr    bool
	}{
		{"no scan line", "", "none", false, num(100), time.Time{}, false},
		{"none requested", "none requested", "none", false, num(100), time.Time{}, false},
		{"finished scrub", "scrub repaired 0B in 00:12:33 with 0 errors on Sun Nov 10 00:36:35 2024", "none", false, num(100), end, false},
		{"finished scrub 0.8", "scrub repaired 0B in 0 days 00:12:33 with 0 errors on Sun Nov 10 00:36:35 2024", "none", false, num(100), end, false},
		{"finished scrub, bad date", "scrub repaired 0B in 00:12:33 with 0 errors on Sonntag 10.11.2024", "none", false, num(100), time.Time{}, true},
		{"canceled", "scrub canceled on Sun Nov 10 00:30:00 2024", "none", false, num(100), time.Time{}, false},
		{"resilvered", "resilvered 1.21T in 03:12:45 with 0 errors on Sat Nov  9 18:02:11 2024", "none", false, num(100), time.Time{}, false},
		{"rebuilt", "resilvered (mirror-0) 1.21T in 00:42:10 with 0 errors on Sat Nov  9 18:02:11 2024", "none", false, num(100), time.Time{}, false},
		{"resilvered, then a scrub line", "resilvered 1.21T in 03:12:45 with 0 errors on Sat Nov  9 18:02:11 2024\n\tscrub repaired 0B in 00:12:33 with 0 errors on Sun Nov 10 00:36:35 2024", "none", false, num(100), end, false},
		{"scrub running", "scrub in progress since Sun Nov 10 00:24:02 2024\n\t1.23T scanned at 1.02G/s, 456G issued at 380M/s, 3.62T total\n\t0B repaired, 12.30% done, 02:25:12 to go", "scrub", false, num(12.30), time.Time{}, false},
		{"scrub running, issued computed", "scrub in progress since Sun Nov 10 00:24:02 2024\n\t1.23T scanned at 1.02G/s, 512G issued at 380M/s, 2T total", "scrub", false, num(25), time.Time{}, false},
		{"scrub running 2.2, issued computed", "scrub in progress since Sun Nov 10 00:24:02 2024\n\t1.5T / 2T scanned at 1.02G/s, 512G / 2T issued at 380M/s", "scrub", false, num(25), time.Time{}, false},
		{"scrub running 2.2, scanned computed", "scrub in progress since Sun Nov 10 00:24:02 2024\n\t1T / 2T scanned at 1.02G/s", "scrub", false, num(50), time.Time{}, false},
		{"scrub running, nothing to compute from", "scrub in progress since Sun Nov 10 00:24:02 2024", "scrub", false, numErr, time.Time{}, false},
		{"scrub running, nan", "scrub in progress since Sun Nov 10 00:24:02 2024\n\t0B repaired, -nan% done, no estimated completion time", "scrub", false, numErr, time.Time{}, false},
		{"scrub running, over 100", "scrub in progress since Sun Nov 10 00:24:02 2024\n\t0B repaired, 100.40% done, 00:00:01 to go", "scrub", false, num(100), time.Time{}, false},
		{"resilver running", "resilver in progress since Sat Nov  9 22:14:51 2024\n\t228G resilvered, 24.61% done, 01:17:52 to go", "resilver", false, num(24.61), time.Time{}, false},
		{"rebuild running", "resilver (draid2:5d:12c:1s-0) in progress since Sat Nov  9 22:14:51 2024\n\t1.82T scanned at 1.21G/s, 912G issued 607M/s, 3.62T total\n\t228G resilvered, 24.61% done, 01:17:52 to go", "resilver", false, num(24.61), time.Time{}, false},
		{"paused", "scrub paused since Sun Nov 10 02:00:00 2024\n\tscrub started on Sun Nov 10 00:24:02 2024\n\t1.23T scanned, 456G issued, 3.62T total\n\t0B repaired, 12.30% done", "scrub_paused", false, num(12.30), time.Time{}, false},
		{"paused, no percentage", "scrub paused since Sun Nov 10 02:00:00 2024\n\tscrub started on Sun Nov 10 00:24:02 2024", "scrub_paused", false, num(0), time.Time{}, false},
		{"unknown", "frobnicate in progress", "", true, numErr, time.Time{}, true},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			var lines []string
			if tc.lines != "" {
				lines = strings.Split(tc.lines, "\n")
			}
			got := parseScan(lines, time.UTC)
			if tc.kindErr {
				if got.kindErr == nil {
					t.Errorf("kind = %q, want an error", got.kind)
				}
			} else if got.kindErr != nil || got.kind != tc.kind {
				t.Errorf("kind = %q (%v), want %q", got.kind, got.kindErr, tc.kind)
			}
			checkNum(t, "pct", got.pct, tc.pct)
			if tc.endErr {
				if got.lastScrubErr == nil {
					t.Errorf("last scrub = %v, want an error", got.lastScrub)
				}
			} else if got.lastScrubErr != nil || !got.lastScrub.Equal(tc.lastScrub) {
				t.Errorf("last scrub = %v (%v), want %v", got.lastScrub, got.lastScrubErr, tc.lastScrub)
			}
		})
	}
}

// TestParseScanLocalTime: the dates are the host's local time.
func TestParseScanLocalTime(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	got := parseScan([]string{"scrub repaired 0B in 00:12:33 with 0 errors on Sun Nov 10 00:36:35 2024"}, loc)
	if want := utc("2024-11-09 23:36:35"); !got.lastScrub.Equal(want) {
		t.Fatalf("last scrub = %v, want %v", got.lastScrub, want)
	}
}
