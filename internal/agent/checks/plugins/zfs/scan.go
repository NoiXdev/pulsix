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
	// names none (never scrubbed, canceled, a resilver), so the age counts
	// from the pool's creation. lastScrubErr is set instead when that end
	// cannot be read.
	lastScrub    time.Time
	lastScrubErr error
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

// parseScan reads a pool's scan paragraph: the text after "scan:" and its
// continuation lines; none at all is a pool that was never scanned.
func parseScan(lines []string, loc *time.Location) scanState {
	st := scanState{kind: scanNone, pct: value(100)}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "scrub repaired ") {
			st.lastScrub, st.lastScrubErr = endDate(l, loc)
			break
		}
	}
	first := ""
	if len(lines) > 0 {
		first = strings.TrimSpace(lines[0])
	}
	switch {
	case first == "" || first == "none requested",
		strings.HasPrefix(first, "scrub repaired "),
		strings.HasPrefix(first, "scrub canceled on "),
		strings.HasPrefix(first, "resilver canceled on "),
		strings.HasPrefix(first, "resilvered "):
		// Nothing runs.
	case strings.HasPrefix(first, "scrub in progress since "):
		st.kind, st.pct = scanScrub, progress(lines, false)
	case resilverRunning.MatchString(first):
		st.kind, st.pct = scanResilver, progress(lines, false)
	case strings.HasPrefix(first, "scrub paused since "):
		st.kind, st.pct = scanScrubPaused, progress(lines, true)
	default:
		err := fmt.Errorf("unrecognized scan line %q", cutText(first, maxQuote))
		st.kind, st.kindErr, st.pct = "", err, number{err: err}
		if st.lastScrub.IsZero() && st.lastScrubErr == nil {
			st.lastScrubErr = err
		}
	}
	return st
}

// endDate reads the date after a line's last " on ".
func endDate(line string, loc *time.Location) (time.Time, error) {
	i := strings.LastIndex(line, " on ")
	if i < 0 {
		return time.Time{}, fmt.Errorf("no date in scan line %q", cutText(line, maxQuote))
	}
	s := strings.TrimSpace(line[i+len(" on "):])
	t, err := time.ParseInLocation(ctimeLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("last scrub's end %q is not a date", cutText(s, maxQuote))
	}
	return t, nil
}

// progress reads a running scan's percentage: the "N% done" zpool prints,
// else computed from the amounts done and in total. A paused scrub that
// prints neither is at 0.
func progress(lines []string, paused bool) number {
	text := strings.Join(lines, " ")
	if m := percentDone.FindStringSubmatch(text); m != nil {
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
