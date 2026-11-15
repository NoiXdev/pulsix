package zfs

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestCollectScrubAge is Review Focus 3: the age from the last finished
// scrub, else from the pool's creation (zfs get, asked only for the pools
// that need it), never an error while zfs get works.
func TestCollectScrubAge(t *testing.T) {
	created := utc("2024-06-01 12:00:00")
	creation := fmt.Sprintf("%%s\t%d\n", created.Unix())
	for _, tc := range []struct {
		status, pool string
		fromCreation bool
		since        time.Time
	}{
		{"status_mirror_healthy.txt", "tank", false, utc("2024-11-10 00:36:35")},
		{"status_scrub_canceled.txt", "tank", true, created},
		{"status_never_scrubbed.txt", "fresh", true, created},
		{"status_resilvered.txt", "tank", true, created},
		{"status_scrub_progress.txt", "tank", true, created},
		{"status_resilver_spare.txt", "tank", true, created},
	} {
		t.Run(tc.status, func(t *testing.T) {
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
