package zfs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// TestCollectScrubAge is Review Focus 3 with the age's sources in order:
// a finished scrub's end; else a running or paused scrub's start; else the
// pool's creation (zfs get, asked only for the pools that need it) — on a
// plugin that remembers nothing yet. Never an error while zfs get works.
func TestCollectScrubAge(t *testing.T) {
	created := utc("2024-06-01 12:00:00")
	creation := fmt.Sprintf("%%s\t%d\n", created.Unix())
	for _, tc := range []struct {
		status, pool string
		fromCreation bool
		since        time.Time
	}{
		{"status_mirror_healthy.txt", "tank", false, utc("2024-11-10 00:36:35")},
		{"status_scrub_progress.txt", "tank", false, utc("2024-11-10 00:24:02")},
		{"status_scrub_paused.txt", "tank", false, utc("2024-11-10 00:24:02")},
		{"status_scrub_canceled.txt", "tank", true, created},
		{"status_never_scrubbed.txt", "fresh", true, created},
		{"status_resilvered.txt", "tank", true, created},
		{"status_resilver_spare.txt", "tank", true, created},
		{"status_rebuild.txt", "alpha", false, utc("2024-11-10 00:36:35")},
		{"status_rebuild.txt", "beta", false, utc("2024-11-17 00:24:02")},
		{"status_rebuild.txt", "gamma", false, utc("2024-11-10 00:36:35")},
		{"status_rebuild.txt", "delta", false, utc("2024-11-17 00:24:02")},
	} {
		t.Run(tc.status+"/"+tc.pool, func(t *testing.T) {
			f := newFake(t, map[string]reply{
				"zpool list":   {stdout: fmt.Appendf(nil, "%s\tONLINE\t100\t50\t50\t1\n", tc.pool)},
				"zpool status": {stdout: load(t, tc.status)},
				"zfs get":      {stdout: fmt.Appendf(nil, creation, tc.pool)},
			})
			obs, err := testPlugin(f).Collect(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			wantValue(t, obs, tc.pool, "zfs.scrub_age_days", days(clock.Sub(tc.since)))
			args, ok := f.called("zfs get")
			if ok != tc.fromCreation {
				t.Fatalf("zfs get called = %v, want %v", ok, tc.fromCreation)
			}
			if ok && !slices.Equal(args, []string{"zfs", "get", "-Hp", "-o", "name,value", "creation", tc.pool}) {
				t.Fatalf("zfs get args = %q", args)
			}
		})
	}
}

// TestCollectScrubAgeRemembered: a finished scrub seen once is remembered
// per pool, so when a resilver, a canceled scrub or an error scrub replaces
// the scan line the age still counts from it, without zfs get; a running
// scrub's start comes first; the memory keeps the later end; a pool whose
// scan line says it was never scanned (re-created or another pool under
// the name) is forgotten and counts from its creation, as does a pool never
// seen finished.
func TestCollectScrubAgeRemembered(t *testing.T) {
	const created = 1717243200
	p := testPlugin(nil)
	round := func(status string, wantGet bool) []checks.Observation {
		t.Helper()
		list := "tank\tONLINE\t100\t50\t50\t1\n"
		if strings.Contains(status, "pool: other\n") {
			list += "other\tONLINE\t100\t50\t50\t1\n"
		}
		f := newFake(t, map[string]reply{
			"zpool list":   {stdout: []byte(list)},
			"zpool status": {stdout: []byte(status)},
			"zfs get":      {stdout: fmt.Appendf(nil, "tank\t%d\nother\t%d\n", created, created)},
		})
		p.run = f
		obs, err := p.Collect(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.called("zfs get"); ok != wantGet {
			t.Fatalf("zfs get called = %v, want %v", ok, wantGet)
		}
		return obs
	}
	other := strings.ReplaceAll(string(load(t, "status_never_scrubbed.txt")), "fresh", "other")
	// withScan is tank's resilvered fixture with another scan paragraph.
	withScan := func(scan string) string {
		return strings.Replace(string(load(t, "status_resilvered.txt")),
			"  scan: resilvered 1.21T in 03:12:45 with 0 errors on Sat Nov  9 18:02:11 2024", scan, 1)
	}
	age := func(s string) float64 { return days(clock.Sub(utc(s))) }
	fromCreation := days(clock.Sub(time.Unix(created, 0)))

	obs := round(string(load(t, "status_mirror_healthy.txt"))+other, true)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", age("2024-11-10 00:36:35"))
	wantValue(t, obs, "other", "zfs.scrub_age_days", fromCreation)

	for _, status := range []string{
		string(load(t, "status_resilvered.txt")),
		string(load(t, "status_scrub_canceled.txt")),
		string(load(t, "status_resilver_spare.txt")),
		// Error scrubs (zpool scrub -e) are not full scrubs.
		withScan(" scrub: error scrub in progress since Sun Nov 17 00:24:02 2024\n\t42.17% done, issued I/O for 1234 error blocks"),
		withScan(" scrub: error scrub paused since Sun Nov 17 02:00:00 2024\n\terror scrub started on Sun Nov 17 00:24:02 2024\n\t42.17% done, issued I/O for 1234 error blocks"),
		withScan(" scrub: scrubbed 1234 error blocks in 0 days 00:00:12 on Sun Nov 17 00:36:35 2024"),
		withScan(" scrub: error scrub canceled on Sun Nov 17 00:30:00 2024"),
	} {
		obs = round(status, false)
		wantValue(t, obs, "tank", "zfs.scrub_age_days", age("2024-11-10 00:36:35"))
	}

	// A running scrub's start before the memory.
	obs = round(string(load(t, "status_scrub_progress.txt")), false)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", age("2024-11-10 00:24:02"))

	// A newer finished scrub replaces the memory; an older one printed
	// later counts for its round but does not send the memory back.
	obs = round(string(load(t, "status_data_errors.txt")), false)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", age("2024-11-10 00:44:13"))
	obs = round(string(load(t, "status_mirror_healthy.txt")), false)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", age("2024-11-10 00:36:35"))
	obs = round(string(load(t, "status_resilvered.txt")), false)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", age("2024-11-10 00:44:13"))

	// Never scanned under this name: forgotten, from its creation — also
	// in the rounds after.
	obs = round(withScan("  scan: none requested"), true)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", fromCreation)
	obs = round(string(load(t, "status_resilvered.txt")), true)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", fromCreation)

	// Seen finished again, then no scan line at all: forgotten too.
	round(string(load(t, "status_mirror_healthy.txt")), false)
	obs = round(withScan(""), true)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", fromCreation)
}

func TestScrubMemory(t *testing.T) {
	m := newScrubMemory()
	early, late := utc("2024-11-01 00:00:00"), utc("2024-11-10 00:00:00")
	m.remember("tank", late)
	m.remember("tank", early)
	if got, ok := m.get("tank"); !ok || !got.Equal(late) {
		t.Fatalf("after an older end: %v, %v; want %v", got, ok, late)
	}
	m.forget("tank")
	if got, ok := m.get("tank"); ok {
		t.Fatalf("forgotten: %v", got)
	}
	m.remember("tank", early)
	if got, ok := m.get("tank"); !ok || !got.Equal(early) {
		t.Fatalf("after forgetting: %v, %v", got, ok)
	}
	var none *scrubMemory
	none.remember("tank", late)
	none.forget("tank")
	if _, ok := none.get("tank"); ok {
		t.Fatal("a nil memory remembered")
	}
}

// TestCollectScrubAgeConcurrent: one plugin instance collected from several
// checks at once (run with -race).
func TestCollectScrubAgeConcurrent(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\n")},
	})
	p := testPlugin(f)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := p.Collect(context.Background(), nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got, ok := p.scrubs.get("rpool"); !ok || !got.Equal(utc("2024-11-10 00:25:53")) {
		t.Fatalf("remembered rpool = %v, %v", got, ok)
	}
}

// TestCollectCreationPartial: zfs get exits non-zero for one pool (it
// vanished) but prints the other's creation: that one stands, only the
// missing one carries the error.
func TestCollectCreationPartial(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: []byte("fresh\tONLINE\t100\t50\t50\t1\ntank\tONLINE\t100\t50\t50\t1\n")},
		"zpool status": {stdout: append(load(t, "status_never_scrubbed.txt"), load(t, "status_scrub_canceled.txt")...)},
		"zfs get": {stdout: []byte("fresh\t1717243200\n"), err: exitStatus(1),
			stderr: []byte("cannot open 'tank': dataset does not exist\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("want an advisory error")
	}
	args, _ := f.called("zfs get")
	if !slices.Equal(args, []string{"zfs", "get", "-Hp", "-o", "name,value", "creation", "fresh", "tank"}) {
		t.Fatalf("zfs get args = %q", args)
	}
	wantValue(t, obs, "fresh", "zfs.scrub_age_days", days(clock.Sub(time.Unix(1717243200, 0))))
	wantErr(t, obs, "tank", "zfs.scrub_age_days", "zfs: zfs get creation: exit status 1: cannot open 'tank': dataset does not exist")
}

// TestCollectScrubAgeOnlyAsksNeededPools: two pools, one scrubbed: zfs get
// names only the other.
func TestCollectScrubAgeOnlyAsksNeededPools(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := f.called("zfs get")
	if !slices.Equal(args, []string{"zfs", "get", "-Hp", "-o", "name,value", "creation", "tank"}) {
		t.Fatalf("zfs get args = %q", args)
	}
	wantValue(t, obs, "rpool", "zfs.scrub_age_days", days(clock.Sub(utc("2024-11-10 00:25:53"))))
	wantValue(t, obs, "tank", "zfs.scrub_age_days", days(clock.Sub(time.Unix(1717243200, 0))))
	wantValue(t, obs, "", "zfs.pools", 2)
}

func TestCollectScrubAgeFutureIsZero(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: fmt.Appendf(nil, "tank\t%d\n", clock.Add(time.Hour).Unix())},
	})
	p := testPlugin(f)
	p.now = func() time.Time { return utc("2024-11-01 00:00:00") } // before rpool's scrub too
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantValue(t, obs, "rpool", "zfs.scrub_age_days", 0)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", 0)
}

// TestCollectCreationFails: a failing zfs get is an error on the
// never-scrubbed pools' zfs.scrub_age_days and nowhere else.
func TestCollectCreationFails(t *testing.T) {
	for _, tc := range []struct {
		desc string
		r    reply
		want string
	}{
		{"exit status", reply{err: exitStatus(1), stderr: []byte("cannot open 'tank': dataset does not exist\n")},
			"zfs: zfs get creation: exit status 1: cannot open 'tank': dataset does not exist"},
		{"not found", reply{err: fmt.Errorf("zfs %w", errNotFound)}, "zfs: zfs get creation: zfs not found"},
		{"pool missing from the output", reply{stdout: []byte("rpool\t1717243200\n")}, "zfs: pool tank: no creation time in zfs get's output"},
		{"bad value", reply{stdout: []byte("tank\t-\n")}, "zfs: pool tank: creation time \"-\" is not a number"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFake(t, map[string]reply{
				"zpool list":   {stdout: load(t, "list_two_pools.txt")},
				"zpool status": {stdout: load(t, "status_two_pools.txt")},
				"zfs get":      tc.r,
			})
			obs, err := testPlugin(f).Collect(context.Background(), nil)
			if err == nil {
				t.Fatal("want an advisory error")
			}
			wantErr(t, obs, "tank", "zfs.scrub_age_days", tc.want)
			wantValue(t, obs, "rpool", "zfs.scrub_age_days", days(clock.Sub(utc("2024-11-10 00:25:53"))))
			wantLabel(t, obs, "tank", "zfs.scan", "none")
			wantValue(t, obs, "tank", "zfs.errors_read", 0)
		})
	}
}
