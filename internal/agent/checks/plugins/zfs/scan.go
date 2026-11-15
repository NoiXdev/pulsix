package zfs

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The values of zfs.scan.
const (
	scanNone        = "none"
	scanScrub       = "scrub"
	scanResilver    = "resilver"
	scanScrubPaused = "scrub_paused"
)

// scanState is what a pool's scan paragraph says.
type scanState struct {
	// kind is zfs.scan; kindErr is set instead when the paragraph is not
	// one the plugin knows.
	kind    string
	kindErr error
	// pct is zfs.scan_pct.
	pct number
	// lastScrub is when the last scrub finished; zero when the paragraph
	// names none (never scrubbed, canceled, running, a resilver).
	// lastScrubErr is set instead when that end cannot be read.
	lastScrub    time.Time
	lastScrubErr error
	// scrubStart is when the running or paused scrub started; zero when
	// none runs or a paused one names no start. scrubStartErr is set
	// instead when that start cannot be read.
	scrubStart    time.Time
	scrubStartErr error
}

// ctimeLayout is the C library's ctime form, which zpool prints dates in
// (LC_ALL=C); a single-digit day is padded with a space.
const ctimeLayout = "Mon Jan _2 15:04:05 2006"

var (
	resilverRunning = regexp.MustCompile(`^resilver( \([^)]*\))? in progress since `)
	percentDone     = regexp.MustCompile(`(\S+)% done`)
	// progressForms compute the progress when no percentage is printed:
	// what was issued (the actual work since sequential scrubs) before
	// what was scanned, each as zpool prints it in its releases.
	progressForms = []*regexp.Regexp{
		regexp.MustCompile(`(\S+) / (\S+) issued`),                                    // 2.2
		regexp.MustCompile(`(\S+) issued\b[^,]*, (\S+) total`),                        // 0.8 – 2.1
		regexp.MustCompile(`(\S+) / (\S+) scanned`),                                   // 2.2
		regexp.MustCompile(`(\S+) scanned out of (\S+)`),                              // before 0.8
		regexp.MustCompile(`(\S+) scanned\b[^,]*, (?:\S+ issued[^,]*, )?(\S+) total`), // 0.8 – 2.1
	}
	sizeForm = regexp.MustCompile(`^(\d+(?:\.\d+)?)([BKMGTPE]?)$`)
)

// parseScan reads a pool's scan paragraph: one entry per "scan:" header,
// each the text after it and its continuation lines; none at all is a pool
// that was never scanned. OpenZFS prints the scrub's entry, then one per
// top-level vdev with a sequential rebuild (zpool replace -s or attach -s,
// a dRAID distributed spare). The running entry names the kind and the
// progress — a resilver or rebuild before a scrub before a paused scrub —
// and the finished-scrub entry the last scrub's end. An entry the plugin
// does not know makes the kind and progress errors, and the last scrub's
// end too unless a finished-scrub entry names it.
func parseScan(entries [][]string, loc *time.Location) scanState {
	st := scanState{kind: scanNone, pct: value(100)}
	var resilvering, scrubbing, paused []string
	var unknown error
	for _, e := range entries {
		if len(e) == 0 {
			continue
		}
		first := strings.TrimSpace(e[0])
		switch {
		case first == "" || first == "none requested",
			strings.HasPrefix(first, "scrub canceled on "),
			strings.HasPrefix(first, "resilver canceled on "),
			strings.HasPrefix(first, "resilvered "):
			// Nothing runs.
		case strings.HasPrefix(first, "scrub repaired "):
			if st.lastScrub.IsZero() && st.lastScrubErr == nil {
				st.lastScrub, st.lastScrubErr = dateAfter(first, " on ", "last scrub's end", loc)
			}
		case strings.HasPrefix(first, "scrub in progress since "):
			scrubbing = firstOf(scrubbing, e)
		case resilverRunning.MatchString(first):
			resilvering = firstOf(resilvering, e)
		case strings.HasPrefix(first, "scrub paused since "):
			paused = firstOf(paused, e)
		default:
			if unknown == nil {
				unknown = fmt.Errorf("unrecognized scan line %q", cutText(first, maxQuote))
			}
		}
	}
	switch {
	case unknown != nil:
		st.kind, st.kindErr, st.pct = "", unknown, number{err: unknown}
		if st.lastScrub.IsZero() && st.lastScrubErr == nil {
			st.lastScrubErr = unknown
		}
	case resilvering != nil:
		st.kind, st.pct = scanResilver, progress(resilvering, false)
	case scrubbing != nil:
		st.kind, st.pct = scanScrub, progress(scrubbing, false)
	case paused != nil:
		st.kind, st.pct = scanScrubPaused, progress(paused, true)
	}
	switch {
	case scrubbing != nil:
		st.scrubStart, st.scrubStartErr = dateAfter(strings.TrimSpace(scrubbing[0]), " since ", "scrub start", loc)
	case paused != nil:
		for _, l := range paused[1:] {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "scrub started on ") {
				st.scrubStart, st.scrubStartErr = dateAfter(l, " on ", "scrub start", loc)
				break
			}
		}
	}
	return st
}

// firstOf keeps the first entry of a kind.
func firstOf(have, e []string) []string {
	if have != nil {
		return have
	}
	return e
}

// dateAfter reads the date after a line's last sep; what names the date
// in the error.
func dateAfter(line, sep, what string, loc *time.Location) (time.Time, error) {
	i := strings.LastIndex(line, sep)
	if i < 0 {
		return time.Time{}, fmt.Errorf("no date in scan line %q", cutText(line, maxQuote))
	}
	s := strings.TrimSpace(line[i+len(sep):])
	t, err := time.ParseInLocation(ctimeLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not a date", what, cutText(s, maxQuote))
	}
	return t, nil
}

// progress reads a running scan's percentage: the "N% done" zpool prints,
// else computed from the amounts done and in total. A paused scrub that
// prints neither is at 0, and so is a scan that just started, for which
// zpool divides 0 by 0 and prints "nan%" ("-nan%" with glibc).
func progress(lines []string, paused bool) number {
	text := strings.Join(lines, " ")
	if m := percentDone.FindStringSubmatch(text); m != nil {
		if strings.Contains(strings.ToLower(m[1]), "nan") {
			return value(0)
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return number{err: fmt.Errorf("scan progress %q is not a number", cutText(m[1]+"%", maxQuote))}
		}
		return value(min(max(v, 0), 100))
	}
	for _, re := range progressForms {
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		done, ok1 := parseSize(m[1])
		total, ok2 := parseSize(m[2])
		if ok1 && ok2 && total > 0 {
			return value(min(max(done/total*100, 0), 100))
		}
	}
	if paused {
		return value(0)
	}
	return number{err: fmt.Errorf("no progress in the scan lines")}
}

// parseSize reads an amount as zpool abbreviates it: 0B, 512K, 1.23T —
// powers of 1024.
func parseSize(s string) (float64, bool) {
	m := sizeForm.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	unit := m[2]
	if unit == "" {
		unit = "B"
	}
	return v * math.Pow(1024, float64(strings.Index("BKMGTPE", unit))), true
}
