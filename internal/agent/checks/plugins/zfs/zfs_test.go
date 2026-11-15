package zfs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestName(t *testing.T) {
	if New().Name() != "zfs" {
		t.Fatal("name")
	}
}

// TestCollectHealthy: every metric of a healthy pool, the age from the last
// scrub's end, no zfs get, the calls made with their fixed arguments.
func TestCollectHealthy(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_one.txt")},
		"zpool status": {stdout: load(t, "status_mirror_healthy.txt")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantLabel(t, obs, "tank", "zfs.state", "ONLINE")
	wantValue(t, obs, "tank", "zfs.capacity_pct", 19)
	wantValue(t, obs, "tank", "zfs.size_bytes", 3985729650688)
	wantValue(t, obs, "tank", "zfs.free_bytes", 3201863843840)
	wantValue(t, obs, "tank", "zfs.fragmentation_pct", 2)
	wantValue(t, obs, "tank", "zfs.errors_read", 0)
	wantValue(t, obs, "tank", "zfs.errors_write", 0)
	wantValue(t, obs, "tank", "zfs.errors_checksum", 0)
	wantValue(t, obs, "tank", "zfs.data_errors", 0)
	wantValue(t, obs, "tank", "zfs.devices_unhealthy", 0)
	wantLabel(t, obs, "tank", "zfs.scan", "none")
	wantValue(t, obs, "tank", "zfs.scan_pct", 100)
	wantValue(t, obs, "tank", "zfs.scrub_age_days", days(clock.Sub(utc("2024-11-10 00:36:35"))))
	wantValue(t, obs, "", "zfs.pools", 1)
	if len(obs) != 14 {
		t.Errorf("got %d observations, want 13 for the pool and zfs.pools: %+v", len(obs), obs)
	}
	if got := f.callList(); !slices.EqualFunc(got, [][]string{listArgs, statusArgs}, slices.Equal) {
		t.Fatalf("calls = %q", got)
	}
}

// TestCollectNoDevZFS: without /dev/zfs there is no ZFS on the host: zero
// pools, no error, nothing run.
func TestCollectNoDevZFS(t *testing.T) {
	f := newFake(t, nil)
	p := testPlugin(f)
	p.hasZFS = func() (bool, error) { return false, nil }
	obs, err := p.Collect(context.Background(), nil)
	if err != nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	wantValue(t, obs, "", "zfs.pools", 0)
	if c := f.callList(); len(c) != 0 {
		t.Fatalf("calls = %q", c)
	}
}

func TestCollectDevZFSCheckFails(t *testing.T) {
	p := testPlugin(newFake(t, nil))
	p.hasZFS = func() (bool, error) { return false, errors.New("stat /dev/zfs: permission denied") }
	obs, err := p.Collect(context.Background(), nil)
	if err == nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	wantErr(t, obs, "", "zfs.pools", "zfs: stat /dev/zfs: permission denied")
}

// TestCollectNoZpool: /dev/zfs but no zpool program: zero pools, no error.
func TestCollectNoZpool(t *testing.T) {
	f := newFake(t, map[string]reply{"zpool list": {err: fmt.Errorf("zpool %w", errNotFound)}})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err != nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	wantValue(t, obs, "", "zfs.pools", 0)
	if c := f.callList(); len(c) != 1 {
		t.Fatalf("calls = %q", c)
	}
}

func TestCollectNoPools(t *testing.T) {
	f := newFake(t, map[string]reply{"zpool list": {}})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err != nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	wantValue(t, obs, "", "zfs.pools", 0)
	if c := f.callList(); len(c) != 1 {
		t.Fatalf("zpool status run without a pool to report: %q", c)
	}
}

// TestCollectListFails: a failing zpool list is one error on zfs.pools and
// nothing else; the error quotes stderr's first line, cleaned and cut.
func TestCollectListFails(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	for _, tc := range []struct {
		desc   string
		r      reply
		want   string
		prefix bool // want is the error's beginning
	}{
		{"exit status", reply{err: exitStatus(1), stderr: []byte("internal error: Bad file descriptor\nAborted (core dumped)\n")},
			"zfs: zpool list: exit status 1: internal error: Bad file descriptor", false},
		{"exit status, control characters", reply{err: exitStatus(2), stderr: []byte("\x1b[31mcannot\topen\x00 '/dev/zfs'\r\nmore")},
			"zfs: zpool list: exit status 2: [31mcannot open '/dev/zfs'", false},
		{"exit status, no stderr", reply{err: exitStatus(1)}, "zfs: zpool list: exit status 1", false},
		{"exit status, long stderr", reply{err: exitStatus(1), stderr: []byte(long)},
			"zfs: zpool list: exit status 1: " + strings.Repeat("é", 128), false},
		{"oversized", reply{err: errOutputTooLarge}, "zfs: zpool list: output exceeds 1 MiB", false},
		{"unexpected output", reply{stdout: []byte("NAME SIZE\n")}, "zfs: zpool list: unexpected line", true},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFake(t, map[string]reply{"zpool list": tc.r})
			obs, err := testPlugin(f).Collect(context.Background(), nil)
			if err == nil || len(obs) != 1 {
				t.Fatalf("obs=%+v err=%v", obs, err)
			}
			o := find(obs, "", "zfs.pools")
			if o == nil || (!tc.prefix && o.Err != tc.want) || (tc.prefix && !strings.HasPrefix(o.Err, tc.want)) {
				t.Fatalf("zfs.pools = %+v, want error %q", o, tc.want)
			}
			if c := f.callList(); len(c) != 1 {
				t.Fatalf("calls = %q", c)
			}
		})
	}
}

// TestCollectStatusFails: the zpool list metrics stand; every
// status-derived metric of every pool is an error naming the problem.
func TestCollectStatusFails(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {err: exitStatus(1), stderr: []byte("internal error: out of memory\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("err = %v", err)
	}
	for _, pool := range []string{"rpool", "tank"} {
		wantLabel(t, obs, pool, "zfs.state", "ONLINE")
		if o := find(obs, pool, "zfs.size_bytes"); o == nil || o.Value == nil {
			t.Errorf("%s size = %+v", pool, o)
		}
		for _, m := range statusMetrics {
			wantErr(t, obs, pool, m, "zfs: zpool status -p: exit status 1: internal error: out of memory")
		}
	}
	wantValue(t, obs, "", "zfs.pools", 2)
	if _, ok := f.called("zfs get"); ok {
		t.Fatal("zfs get run after zpool status failed")
	}
}

// TestCollectBlockingStatus is Review Focus 1: zpool status hangs (a
// suspended pool). The call's own bound stops it, the error lands on the
// status-derived metrics, and the zpool list metrics stand — also when the
// runner does not return when told to.
func TestCollectBlockingStatus(t *testing.T) {
	for _, tc := range []struct {
		desc string
		r    reply
	}{
		{"blocks until stopped", reply{block: true}},
		{"ignores the stop", reply{ignoreCtx: true}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFake(t, map[string]reply{
				"zpool list":   {stdout: load(t, "list_one.txt")},
				"zpool status": tc.r,
			})
			p := testPlugin(f)
			p.callTimeout = 50 * time.Millisecond
			p.collectTimeout = 5 * time.Second
			start := time.Now()
			obs, err := p.Collect(context.Background(), nil)
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("Collect took %v", d)
			}
			if err == nil {
				t.Fatal("want an advisory error")
			}
			for _, m := range statusMetrics {
				wantErr(t, obs, "tank", m, "zfs: zpool status -p did not answer within 50ms")
			}
			wantLabel(t, obs, "tank", "zfs.state", "ONLINE")
			wantValue(t, obs, "", "zfs.pools", 1)
		})
	}
}

// TestCollectBlockingCollection: the collection's own bound ends a call
// that its per-call bound would still allow.
func TestCollectBlockingCollection(t *testing.T) {
	for _, call := range []string{"zpool list", "zpool status"} {
		t.Run(call, func(t *testing.T) {
			replies := map[string]reply{
				"zpool list":   {stdout: load(t, "list_one.txt")},
				"zpool status": {stdout: load(t, "status_mirror_healthy.txt")},
			}
			replies[call] = reply{ignoreCtx: true}
			p := testPlugin(newFake(t, replies))
			p.callTimeout = time.Minute
			p.collectTimeout = 80 * time.Millisecond
			start := time.Now()
			obs, err := p.Collect(context.Background(), nil)
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("Collect took %v", d)
			}
			if err == nil {
				t.Fatal("want an advisory error")
			}
			if call == "zpool list" {
				if len(obs) != 1 {
					t.Fatalf("obs = %+v", obs)
				}
				wantErr(t, obs, "", "zfs.pools", "zfs: zpool list: no answer within the check's 80ms time limit")
				return
			}
			wantErr(t, obs, "tank", "zfs.scan", "zfs: zpool status -p: no answer within the check's 80ms time limit")
			wantValue(t, obs, "", "zfs.pools", 1)
		})
	}
}

// TestCollectDefaultBounds: 30 s per call, 60 s per collection.
func TestCollectDefaultBounds(t *testing.T) {
	call, collect := zfsPlugin{}.bounds()
	if call != 30*time.Second || collect != 60*time.Second {
		t.Fatalf("bounds = %v, %v", call, collect)
	}
}

// TestCollectCallerCanceled: the agent shutting down ends Collect with
// nothing but the context's error.
func TestCollectCallerCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFake(t, map[string]reply{"zpool list": {block: true}})
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	obs, err := testPlugin(f).Collect(ctx, nil)
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	if obs, err := testPlugin(f).Collect(ctx, nil); !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("dead context: obs=%+v err=%v", obs, err)
	}
}

// TestCollectFilter: the pools parameter as raid's arrays: a listed pool
// that does not exist reports nothing, a repeated one once.
func TestCollectFilter(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{"pools": []any{"tank", "tank", "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Item == "rpool" || o.Item == "nope" {
			t.Fatalf("filtered pool reported: %+v", o)
		}
	}
	n := 0
	for _, o := range obs {
		if o.Item == "tank" && o.Metric == "zfs.state" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("tank state reported %d times", n)
	}
	wantValue(t, obs, "", "zfs.pools", 1)

	// Nothing left after the filter: no zpool status.
	f = newFake(t, map[string]reply{"zpool list": {stdout: load(t, "list_two_pools.txt")}})
	obs, err = testPlugin(f).Collect(context.Background(), map[string]any{"pools": []string{"nope"}})
	if err != nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	wantValue(t, obs, "", "zfs.pools", 0)
}

func TestCollectBadParam(t *testing.T) {
	f := newFake(t, nil)
	if _, err := testPlugin(f).Collect(context.Background(), map[string]any{"pools": 5}); err == nil {
		t.Fatal("want error")
	}
}

// TestCollectUnreportablePool is Review Focus 5: a pool name the manager
// would refuse is skipped, named in the advisory error, not counted and
// not asked for.
func TestCollectUnreportablePool(t *testing.T) {
	bad := "bad\x7fname"
	f := newFake(t, map[string]reply{
		"zpool list": {stdout: []byte(bad + "\tONLINE\t1\t1\t1\t1\ntank\tONLINE\t100\t50\t50\t1\n")},
		"zpool status": {stdout: []byte("  pool: " + bad + "\n state: ONLINE\n  scan: none requested\n" +
			string(load(t, "status_mirror_healthy.txt")))},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be reported") {
		t.Fatalf("err = %v", err)
	}
	for _, o := range obs {
		if o.Item == bad {
			t.Fatalf("unreportable pool reported: %+v", o)
		}
	}
	wantValue(t, obs, "", "zfs.pools", 1)
	if _, ok := f.called("zfs get"); ok {
		t.Fatal("zfs get asked for the unreportable pool")
	}
}

// TestCollectPoolVanished is Review Focus 5: a pool in zpool list missing
// from zpool status gets its status-derived metrics as errors; a pool only
// in zpool status (created between the calls) is ignored.
func TestCollectPoolVanished(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: append(load(t, "status_never_scrubbed.txt"), load(t, "status_mirror_healthy.txt")...)},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("want an advisory error")
	}
	for _, m := range statusMetrics {
		wantErr(t, obs, "rpool", m, "zfs: pool rpool: missing from zpool status")
	}
	wantLabel(t, obs, "rpool", "zfs.state", "ONLINE")
	wantValue(t, obs, "tank", "zfs.errors_read", 0)
	if find(obs, "fresh", "zfs.state") != nil || find(obs, "fresh", "zfs.scan") != nil {
		t.Fatal("pool only in zpool status reported")
	}
	if _, ok := f.called("zfs get"); ok {
		t.Fatal("zfs get asked for a pool missing from zpool status")
	}
	wantValue(t, obs, "", "zfs.pools", 2)
}

// TestCollectUnavailablePool: '-' for fragmentation leaves the metric out;
// '-' for size, free and capacity (an unavailable pool) is their error.
func TestCollectUnavailablePool(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_unavail.txt")},
		"zpool status": {stdout: load(t, "status_mirror_healthy.txt")},
	})
	obs, _ := testPlugin(f).Collect(context.Background(), nil)
	wantLabel(t, obs, "backup", "zfs.state", "UNAVAIL")
	for _, m := range []string{"zfs.size_bytes", "zfs.free_bytes", "zfs.capacity_pct"} {
		wantErr(t, obs, "backup", m, "zfs: pool backup: zpool list prints no")
	}
	if o := find(obs, "backup", "zfs.fragmentation_pct"); o != nil {
		t.Fatalf("fragmentation = %+v, want absent", o)
	}

	f = newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_old.txt")},
		"zpool status": {stdout: load(t, "status_old.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\n")},
	})
	obs, _ = testPlugin(f).Collect(context.Background(), nil)
	if o := find(obs, "backup", "zfs.fragmentation_pct"); o != nil {
		t.Fatalf("fragmentation = %+v, want absent", o)
	}
	wantValue(t, obs, "backup", "zfs.capacity_pct", 50)
	wantValue(t, obs, "backup", "zfs.errors_read", 1229)
	wantValue(t, obs, "tank", "zfs.scan_pct", 1.23/3.62*100)
	wantLabel(t, obs, "tank", "zfs.scan", "scrub")
}

// TestCollectStatusParseErrors: a pool's own unreadable parts are errors on
// their metrics; zfs.scan still names the kind when only its percentage
// does not parse.
func TestCollectStatusParseErrors(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: []byte("backup\tFAULTED\t100\t50\t50\t1\n")},
		"zpool status": {stdout: []byte(strings.Replace(string(load(t, "status_faulted.txt")), "  scan: scrub repaired 0B in 01:02:03 with 0 errors on Sun Oct 13 01:26:04 2024", "  scan: scrub in progress since Sun Nov 10 00:24:02 2024\n\t0B repaired, -nan% done", 1))},
		"zfs get":      {stdout: []byte("backup\t1717243200\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("want an advisory error")
	}
	wantLabel(t, obs, "backup", "zfs.state", "FAULTED")
	wantErr(t, obs, "backup", "zfs.data_errors", "List of errors unavailable: pool I/O is currently suspended")
	wantLabel(t, obs, "backup", "zfs.scan", "scrub")
	wantErr(t, obs, "backup", "zfs.scan_pct", "-nan")
	wantValue(t, obs, "backup", "zfs.devices_unhealthy", 1)
	wantValue(t, obs, "backup", "zfs.errors_write", 12)
}

// TestCollectAllOrNothingPerMetric: every reported pool carries every
// metric on every run (fragmentation aside), each exactly once.
func TestCollectAllOrNothingPerMetric(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: []byte("fast\tONLINE\t100\t50\t50\t1\n")},
		"zpool status": {stdout: load(t, "status_groups.txt")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zfs.state", "zfs.capacity_pct", "zfs.size_bytes", "zfs.free_bytes", "zfs.fragmentation_pct"}
	want = append(want, statusMetrics...)
	for _, m := range want {
		n := 0
		for _, o := range obs {
			if o.Item == "fast" && o.Metric == m {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s reported %d times", m, n)
		}
	}
	wantValue(t, obs, "fast", "zfs.errors_read", 10)
	wantValue(t, obs, "fast", "zfs.errors_write", 3)
	wantValue(t, obs, "fast", "zfs.errors_checksum", 13)
	wantValue(t, obs, "fast", "zfs.devices_unhealthy", 1)
}
