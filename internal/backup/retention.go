package backup

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Retention says how many daily, weekly and monthly scheduled backups to
// keep. Zero turns a tier off; the newest scheduled backup is kept anyway.
type Retention struct {
	Daily, Weekly, Monthly int
}

// Prune returns the names of the scheduled objects to delete under a
// grandfather-father-son policy: the newest object of each of the Daily
// newest local days, Weekly newest ISO weeks and Monthly newest months (all
// in loc, UTC when nil) is kept. The names come back newest first.
//
// Only Verified scheduled objects created at or before now take part: an
// object whose header could not be read or authenticated, or that claims a
// later time, is never returned and never takes a bucket from a real
// backup. Manual objects, the newest scheduled object and keep (the backup
// the current run wrote) are never returned either.
func Prune(objs []Object, r Retention, loc *time.Location, keep string, now time.Time) (remove []string) {
	if loc == nil {
		loc = time.UTC
	}

	type entry struct {
		name string
		at   time.Time
	}
	var scheduled []entry
	for _, o := range objs {
		// A damaged, unauthenticated or future-dated object is left alone
		// entirely: it must not be removed, and must not fill a bucket or
		// count as the newest backup either.
		if !o.Verified || o.HeaderErr != nil {
			continue
		}
		if at, kind, ok := o.created(); ok && kind == KindScheduled && !at.After(now) {
			scheduled = append(scheduled, entry{o.Name, at})
		}
	}
	slices.SortFunc(scheduled, func(a, b entry) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return strings.Compare(b.name, a.name)
	})

	kept := make(map[string]bool)
	tier := func(limit int, bucket func(time.Time) string) {
		seen := make(map[string]bool)
		for _, e := range scheduled {
			if len(seen) >= limit {
				return
			}
			b := bucket(e.at.In(loc))
			if seen[b] {
				continue // an older object of a bucket that is already kept
			}
			seen[b] = true
			kept[e.name] = true
		}
	}
	tier(r.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	tier(r.Weekly, func(t time.Time) string {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	})
	tier(r.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	if len(scheduled) > 0 {
		kept[scheduled[0].name] = true
	}

	for _, e := range scheduled {
		if !kept[e.name] && e.name != keep {
			remove = append(remove, e.name)
		}
	}
	return remove
}
