// Package zfs is the agent's ZFS pool health check plugin, Linux only. One
// item per pool with zfs.state (a label: the pool's health as zpool prints
// it — ONLINE, DEGRADED, FAULTED, OFFLINE, UNAVAIL, REMOVED, SUSPENDED),
// zfs.capacity_pct, zfs.size_bytes, zfs.free_bytes, zfs.fragmentation_pct,
// zfs.errors_read, zfs.errors_write, zfs.errors_checksum, zfs.data_errors,
// zfs.devices_unhealthy, zfs.scan (a label: none, scrub, resilver,
// scrub_paused), zfs.scan_pct and zfs.scrub_age_days; item "" carries
// zfs.pools. The Plugin contract and the shared observation helpers it
// builds on are in internal/agent/checks.
//
// Sources: the zpool and zfs programs, looked up in /usr/sbin, /sbin and
// /usr/local/sbin before PATH (lookup.go) and executed directly with a
// fixed argument list, never through a shell, with LC_ALL=C (runner.go).
// Their text output is read, not the JSON output, which needs a newer
// OpenZFS than the current distributions ship. Calls, in order:
// `zpool list -Hp -o name,health,size,free,capacity,fragmentation`
// (list.go); `zpool status -p` for every pool, the pools filter applied
// after parsing (status.go, scan.go); and only for the reported pools
// whose scrub age counts from their creation (below),
// `zfs get -Hp -o name,value creation <pool>…` (age.go). Each call gets
// 30 s and stdout up to 1 MiB, the whole collection 60 s.
//
// Every metric is reported on every run for every reported pool — a value
// the agent stops sending turns stale and leaves the host unknown — as a
// value or as a collection error, except zfs.fragmentation_pct where zpool
// prints '-': a pool without the space map histogram feature never has
// it, so it is never stale; an unavailable pool's (every column but name
// and health '-') turns stale alongside the errors on its size, free space
// and capacity. zpool status may
// print several scan: entries for a pool — the scrub's (or, OpenZFS 2.2,
// an error scrub's under " scrub:" when that is the newer), then one per
// top-level vdev with a sequential rebuild, running, finished or canceled
// — and the running one names zfs.scan (a resilver or rebuild before a
// scrub or error scrub before a paused one).
// zfs.scan_pct is its progress (computed from the amounts done and in
// total when no percentage is printed; 0 for the "nan%" of a scan that just
// started), the paused scrub's (0 when it prints none), 100 when none runs.
//
// zfs.scrub_age_days is in days, fractional, counted from the first of:
// the end of the finished scrub zpool status prints now (the plugin
// remembers it per pool, keeping the later end); the start of the running
// or paused scrub, so a routine scrub reads about 0 days rather than the
// pool's age; the remembered end, when a resilver, a canceled scrub or an
// error scrub has replaced the scan line since; the pool's creation. A
// canceled scrub is not a finished one, a resilver is not a scrub, an
// error scrub (zpool scrub -e) is no full scrub, and a pool that was never
// scrubbed is not an error. A pool whose scan paragraph is empty or "none
// requested" while it is ONLINE or DEGRADED was never scanned under this
// identity (the record is kept on disk and never reset: the pool was
// re-created, or another one took the name), so its remembered end is
// forgotten; an unavailable pool prints no scan line and keeps it. zfs get
// is asked only about ONLINE and DEGRADED pools (it may hang on a
// suspended one); another pool whose age would count from its creation
// reports an error naming its health instead. The memory lives in the
// plugin only: after an agent restart the age counts from the creation (or
// a running scrub's start) until the next finished scrub is seen. A date
// in the future (the clock moved) reads 0.
//
// Error counters sum each column over every row of the pool's
// configuration tree — the pool row, vdevs, leaves and the logs, cache,
// special and dedup groups' rows — since each row counts its own errors,
// not its children's. The spares group's rows carry none. Leaves (rows with
// no deeper row beneath them) that are not ONLINE count as
// zfs.devices_unhealthy, the spares group's AVAIL and INUSE rows aside: a
// spare that stands in appears in the vdev it replaces and counts there.
//
// Faults: a failing zpool list (exit status, timeout, oversized output) is
// one collection error on zfs.pools and nothing else, so the pools'
// values turn stale as when the agent cannot run. A failing zpool status
// keeps the zpool list metrics and makes every status-derived metric of
// every pool an error; a pool in zpool list missing from zpool status
// (it vanished between the calls) gets those errors on its own; a pool
// only in zpool status is ignored. A failing zfs get makes
// zfs.scrub_age_days an error for the pools it printed no creation time
// for; the ones it did print stand. Error texts
// quote at most the first line of stderr, cleaned and cut to 256 bytes.
//
// Pools named in the "pools" parameter (a list; only the imported pools it
// names are reported, every imported pool without it) or in
// "optional_pools" (a list too; it never narrows: with "pools" empty every
// imported pool is reported besides the optional ones) that zpool list does
// not print report exactly one observation, zfs.state: EXPORTED for a name
// in optional_pools (also when in both lists), MISSING for one only in
// pools. Labels are upper case like zpool's own. Neither is asked about
// with zpool status or zfs get, and a failing zpool list, where nothing is
// known to be absent, reports none of them. The scrub memory of a pool
// that is absent is kept: it may come back.
//
// zfs.pools is the number of imported pools reported this round, after the
// "pools" filter, on every run, also 0; EXPORTED and MISSING pools are not
// counted. A host without /dev/zfs (no ZFS module)
// or without zpool has zero pools and no error. A pool name the manager
// would refuse as an item is skipped and named in the advisory error. Off
// Linux the plugin reports one collection error on zfs.pools: not
// supported on this platform.
package zfs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/checkplugins"
)

const (
	defaultCallTimeout    = 30 * time.Second
	defaultCollectTimeout = 60 * time.Second
)

// poolsMetric is the host-level count on item "" that also carries every
// failure concerning no single pool.
const poolsMetric = "zfs.pools"

// zfsPlugin reports the host's ZFS pools.
type zfsPlugin struct {
	// hasZFS reports whether the ZFS module is loaded (/dev/zfs exists);
	// off Linux it fails with "not supported on this platform".
	hasZFS func() (bool, error)
	run    runner
	// now is the clock the scrub age is counted on; nil means time.Now.
	now func() time.Time
	// loc is the time zone zpool prints dates in; nil means time.Local.
	loc *time.Location
	// scrubs remembers each pool's last finished scrub; shared by every
	// copy of the plugin value. nil remembers nothing.
	scrubs *scrubMemory
	// callTimeout and collectTimeout override the defaults; zero means the
	// default.
	callTimeout    time.Duration
	collectTimeout time.Duration
}

// New returns the zfs plugin wired to the host's /dev/zfs and ZFS
// programs (a stub off Linux).
func New() checks.Plugin {
	return zfsPlugin{
		hasZFS: zfsLoaded,
		run:    execRunner{find: locator{lookPath: exec.LookPath}.find, busy: newInflight()},
		now:    time.Now,
		scrubs: newScrubMemory(),
	}
}

func (zfsPlugin) Name() string { return "zfs" }

// bounds returns the per-call and per-collection time limits.
func (p zfsPlugin) bounds() (call, collect time.Duration) {
	call, collect = p.callTimeout, p.collectTimeout
	if call <= 0 {
		call = defaultCallTimeout
	}
	if collect <= 0 {
		collect = defaultCollectTimeout
	}
	return call, collect
}

// Collect reads the pools. The plugin's own deadlines firing is a slow or
// hanging zpool: what was gathered is reported, the rest as error
// observations. The caller's context ending is the agent shutting down:
// Collect then returns nothing but ctx.Err(), per the Plugin contract.
func (p zfsPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filter, _, err := checks.ParamStrings(params, "pools")
	if err != nil {
		return nil, fmt.Errorf("zfs: %w", err)
	}
	optional, _, err := checks.ParamStrings(params, "optional_pools")
	if err != nil {
		return nil, fmt.Errorf("zfs: %w", err)
	}
	if p.hasZFS == nil {
		return poolCount(0), nil // a zero plugin: no ZFS to look at
	}
	loaded, err := p.hasZFS()
	if err != nil {
		return hostError(fmt.Errorf("zfs: %w", err))
	}
	if !loaded {
		return poolCount(0), nil // no ZFS on this host
	}

	callTimeout, collectTimeout := p.bounds()
	cctx, cancel := context.WithTimeout(ctx, collectTimeout)
	defer cancel()
	c := caller{ctx: cctx, run: p.run, callTimeout: callTimeout, collectTimeout: collectTimeout}

	out, err := c.call("zpool", zpoolListArgs, "zpool list")
	if perr := ctx.Err(); perr != nil {
		return nil, perr
	}
	if errors.Is(err, errNotFound) {
		return poolCount(0), nil // ZFS module without its programs
	}
	if err != nil {
		return hostError(err)
	}
	listed, err := parseList(out)
	if err != nil {
		return hostError(fmt.Errorf("zfs: zpool list: %w", err))
	}
	pools, absent, errs := selectPools(listed, filter, optional)
	if len(pools) == 0 {
		r := newReport(errs)
		r.absent(absent)
		r.obs = append(r.obs, checks.ValueObservation("", poolsMetric, 0))
		return r.obs, r.err()
	}

	var statuses map[string]poolStatus
	out, statusErr := c.call("zpool", zpoolStatusArgs, "zpool status -p")
	if perr := ctx.Err(); perr != nil {
		return nil, perr
	}
	if statusErr == nil {
		statuses = parseStatus(out, p.location())
	}

	bases, fromCreation := scrubBases(pools, statuses, p.scrubs)
	var created creationTimes
	if len(fromCreation) > 0 {
		created = c.creation(fromCreation)
		if perr := ctx.Err(); perr != nil {
			return nil, perr
		}
	}

	r := newReport(errs)
	now := p.clock()
	for _, lp := range pools {
		r.pool(lp, statuses, statusErr, scrubAge{base: bases[lp.name], created: created, now: now})
	}
	r.absent(absent)
	r.obs = append(r.obs, checks.ValueObservation("", poolsMetric, float64(len(pools))))
	return r.obs, r.err()
}

func (p zfsPlugin) clock() time.Time {
	if p.now == nil {
		return time.Now()
	}
	return p.now()
}

func (p zfsPlugin) location() *time.Location {
	if p.loc == nil {
		return time.Local
	}
	return p.loc
}

// hostError reports a failure that concerns no single pool on item "".
func hostError(err error) ([]checks.Observation, error) {
	return []checks.Observation{checks.ErrObservation("", poolsMetric, err)}, err
}

func poolCount(n int) []checks.Observation {
	return []checks.Observation{checks.ValueObservation("", poolsMetric, float64(n))}
}

// Labels of a named pool that zpool list does not print, upper case like
// zpool's own health values. They are the manager's descriptor constants:
// the zfs descriptor holds an absent pool's other metrics (HeldWhile) on
// exactly these labels (backlog 3.74).
const (
	stateExported = checkplugins.ZFSStateExported // absent, and named in optional_pools
	stateMissing  = checkplugins.ZFSStateMissing  // absent, and named only in pools
)

// absentPool is a pool named in a parameter that zpool list does not print.
type absentPool struct{ name, state string }

// selectPools keeps the listed pools asked for, each once, and skips a name
// the manager would refuse as an item. Without any list every listed pool
// is asked for; "pools" narrows to the pools it names (and the optional
// ones); "optional_pools" alone narrows nothing. A named pool that is not
// listed is returned as absent, in the order the names are given: EXPORTED
// when optional (also when named in both lists), else MISSING.
func selectPools(listed []listedPool, filter, optional []string) ([]listedPool, []absentPool, []error) {
	var want map[string]bool
	if len(filter) > 0 {
		want = make(map[string]bool, len(filter)+len(optional))
		for _, s := range filter {
			want[s] = true
		}
		for _, s := range optional {
			want[s] = true
		}
	}
	var (
		pools []listedPool
		errs  []error
		seen  = map[string]bool{}
	)
	for _, lp := range listed {
		if (want != nil && !want[lp.name]) || seen[lp.name] {
			continue
		}
		seen[lp.name] = true
		if !checks.ReportableItem(lp.name) {
			errs = append(errs, unreportable(lp.name))
			continue
		}
		pools = append(pools, lp)
	}

	isOptional := make(map[string]bool, len(optional))
	for _, s := range optional {
		isOptional[s] = true
	}
	var absent []absentPool
	for _, name := range append(slices.Clone(filter), optional...) {
		if seen[name] {
			continue
		}
		seen[name] = true
		if !checks.ReportableItem(name) {
			errs = append(errs, unreportable(name))
			continue
		}
		state := stateMissing
		if isOptional[name] {
			state = stateExported
		}
		absent = append(absent, absentPool{name: name, state: state})
	}
	return pools, absent, errs
}

func unreportable(name string) error {
	return fmt.Errorf("zfs: pool %q cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a control character", name, checks.MaxItemLen)
}

var _ checks.Plugin = zfsPlugin{}
