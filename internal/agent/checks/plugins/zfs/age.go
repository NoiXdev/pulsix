package zfs

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// zfsGetArgs is `zfs get` for the creation time (a Unix time with -p) of
// the pools' root datasets, which follow as further arguments.
var zfsGetArgs = []string{"get", "-Hp", "-o", "name,value", "creation"}

// ageBase is where a pool's zfs.scrub_age_days counts from: since, or err
// when that cannot be read, or the pool's creation (fromCreation).
type ageBase struct {
	since        time.Time
	err          error
	fromCreation bool
}

// scrubBase picks the age's base, in order: the finished scrub zpool
// prints now; the running or paused scrub's start, so a routine scrub
// reads about 0 days rather than the pool's age; the finished scrub
// remembered from an earlier round (a resilver, a canceled scrub or
// nothing has replaced the line since); else the pool's creation.
func scrubBase(scan scanState, remembered time.Time, haveRemembered bool) ageBase {
	switch {
	case scan.lastScrubErr != nil:
		return ageBase{err: scan.lastScrubErr}
	case !scan.lastScrub.IsZero():
		return ageBase{since: scan.lastScrub}
	case scan.scrubStartErr != nil:
		return ageBase{err: scan.scrubStartErr}
	case !scan.scrubStart.IsZero():
		return ageBase{since: scan.scrubStart}
	case haveRemembered:
		return ageBase{since: remembered}
	}
	return ageBase{fromCreation: true}
}

// scrubBases remembers the finished scrubs zpool status prints for the
// reported pools, forgets the pools it says were never scanned (only while
// zpool list shows them ONLINE or DEGRADED: an unavailable pool shown from
// the cache file prints no scan line), and returns each pool's age base and
// the pools whose age counts from their creation (zfs get is asked for
// those only, and only while they are ONLINE or DEGRADED; another pool's
// age is then an error naming its health).
func scrubBases(pools []listedPool, statuses map[string]poolStatus, mem *scrubMemory) (map[string]ageBase, []string) {
	bases := map[string]ageBase{}
	var fromCreation []string
	for _, lp := range pools {
		st, ok := statuses[lp.name]
		if !ok {
			continue
		}
		available := lp.health == "ONLINE" || lp.health == "DEGRADED"
		switch {
		case st.scan.neverScanned && available:
			mem.forget(lp.name) // re-created, or another pool under the name
		case !st.scan.lastScrub.IsZero() && st.scan.lastScrubErr == nil:
			mem.remember(lp.name, st.scan.lastScrub)
		}
		end, have := mem.get(lp.name)
		b := scrubBase(st.scan, end, have)
		switch {
		case b.fromCreation && !available:
			// zfs get may hang on a suspended pool and cost every other
			// pool its age: not asked.
			b = ageBase{err: fmt.Errorf("pool is %s: creation time not read", lp.health)}
		case b.fromCreation:
			fromCreation = append(fromCreation, lp.name)
		}
		bases[lp.name] = b
	}
	return bases, fromCreation
}

// creationTimes is zfs get's answer: each pool's raw creation value, and
// the call's error, which a pool it printed no value for carries.
type creationTimes struct {
	values map[string]string
	err    error
}

// creation asks zfs get for the named pools' creation times.
func (c caller) creation(names []string) creationTimes {
	out, err := c.call("zfs", append(slices.Clone(zfsGetArgs), names...), "zfs get creation")
	values := map[string]string{}
	for line := range bytes.Lines(out) {
		name, v, ok := strings.Cut(strings.TrimRight(string(line), "\r\n"), "\t")
		if ok {
			values[name] = v
		}
	}
	return creationTimes{values: values, err: err}
}

// of returns the pool's creation time.
func (ct creationTimes) of(name string) (time.Time, error) {
	v, ok := ct.values[name]
	switch {
	case !ok && ct.err != nil:
		return time.Time{}, ct.err
	case !ok:
		return time.Time{}, fmt.Errorf("zfs: pool %s: no creation time in zfs get's output", name)
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("zfs: pool %s: creation time %q is not a number", name, cutText(v, maxQuote))
	}
	return time.Unix(sec, 0), nil
}

// scrubAge is one pool's age base with the creation times, when asked.
type scrubAge struct {
	base    ageBase
	created creationTimes
	now     time.Time
}

// scrubAge reports zfs.scrub_age_days.
func (r *report) scrubAge(name string, a scrubAge) {
	const metric = "zfs.scrub_age_days"
	since, err := a.base.since, a.base.err
	if err != nil {
		err = fmt.Errorf("zfs: pool %s: %w", name, err)
	} else if a.base.fromCreation {
		since, err = a.created.of(name)
	}
	if err != nil {
		r.fail(name, metric, err)
		return
	}
	r.obs = append(r.obs, checks.ValueObservation(name, metric, daysSince(a.now, since)))
}

// daysSince is the fractional days from t to now; 0 when t is later.
func daysSince(now, t time.Time) float64 {
	return max(now.Sub(t).Hours()/24, 0)
}
